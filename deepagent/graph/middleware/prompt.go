package middleware

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	filesystempkg "eino-cli/deepagent/graph/filesystem"
	skillspkg "eino-cli/deepagent/graph/skills"
	"eino-cli/deepagent/graph/tools"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const BasePromptMiddlewareName = "base_prompt"

// BasePromptMiddleware supplies the initial system context for a model run.
type BasePromptMiddleware struct {
	BaseMiddleware
	prompt string
}

func NewBasePromptMiddleware(prompt string) *BasePromptMiddleware {
	return &BasePromptMiddleware{prompt: prompt}
}

func (m *BasePromptMiddleware) Name() string { return BasePromptMiddlewareName }

func (m *BasePromptMiddleware) BuildInitialContext(context.Context) ([]*schema.Message, error) {
	if m == nil || m.prompt == "" {
		return nil, nil
	}
	return []*schema.Message{schema.SystemMessage(m.prompt)}, nil
}

func (m *BasePromptMiddleware) BuildPrompt(ctx context.Context) ([]*schema.Message, error) {
	return m.BuildInitialContext(ctx)
}

type projectInstructions struct {
	BaseMiddleware
	files filesystempkg.Filesystem
}

func NewProjectInstructions(files filesystempkg.Filesystem) Middleware {
	return &projectInstructions{files: files}
}

func (*projectInstructions) Name() string { return "project_instructions" }

func (m *projectInstructions) BuildPrompt(ctx context.Context) ([]*schema.Message, error) {
	// Read the complete bounded backend file rather than the tool's default
	// 2000-line window; the requested section may occur near the end.
	limit := int(^uint(0) >> 1)
	text, err := m.files.Read(ctx, "AGENTS.md", nil, &limit)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, filesystempkg.ErrFileNotFound) {
			return nil, nil
		}
		return nil, err
	}
	body := projectInstructionSection(text, "Agent Working Discipline")
	if body == "" {
		return nil, nil
	}
	return []*schema.Message{schema.SystemMessage("<agent_discipline>\n" + body + "\n</agent_discipline>")}, nil
}

func projectInstructionSection(text, title string) string {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	start := -1
	for i, line := range lines {
		if start < 0 {
			if strings.TrimSpace(line) == "## "+title {
				start = i + 1
			}
			continue
		}
		if strings.HasPrefix(line, "## ") {
			return strings.TrimSpace(strings.Join(lines[start:i], "\n"))
		}
	}
	if start >= 0 {
		return strings.TrimSpace(strings.Join(lines[start:], "\n"))
	}
	return ""
}

type SkillMiddleware struct {
	BaseMiddleware
	loader skillspkg.SkillLoader
}

func NewSkillMiddleware(loader skillspkg.SkillLoader) Middleware {
	return &SkillMiddleware{loader: loader}
}

func (m *SkillMiddleware) Name() string { return "skill" }

func (m *SkillMiddleware) BuildPrompt(ctx context.Context) ([]*schema.Message, error) {

	if m == nil || m.loader == nil {
		return nil, nil
	}

	items, err := m.loader.ListSkills(ctx)
	if err != nil {
		return nil, err
	}
	skillMetaList := make([]*skillspkg.SkillMetadata, 0, len(items))
	for _, item := range items {
		if item != nil && strings.TrimSpace(item.Name) != "" {
			skillMetaList = append(skillMetaList, item)
		}
	}

	sort.Slice(skillMetaList, func(i, j int) bool { return skillMetaList[i].Name < skillMetaList[j].Name })

	var prompt strings.Builder
	prompt.WriteString("Available project skills. When a skill applies, call activate_skill with its exact name before using it. The tool returns its full instructions and source path.\n")
	for _, item := range skillMetaList {
		fmt.Fprintf(&prompt, "- %s: %s (source: %s)\n", item.Name, item.Description, item.Path)
	}
	return []*schema.Message{schema.SystemMessage(prompt.String())}, nil
}

func (m *SkillMiddleware) Tools(context.Context) ([]tool.BaseTool, error) {
	if m == nil || m.loader == nil {
		return nil, nil
	}
	return []tool.BaseTool{tools.NewActivateSkillTool(m.loader)}, nil
}
