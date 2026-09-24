package middleware

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/core/tools"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"sort"
	"strings"
)

type Skill struct {
	BaseMiddleware
	loader backend.SkillLoader
}

func NewSkill(loader backend.SkillLoader) Middleware { return &Skill{loader: loader} }
func (m *Skill) Name() string                        { return "skill" }

func (m *Skill) BuildPrompt(ctx context.Context) ([]*schema.Message, error) {
	skills, err := m.list(ctx)
	if err != nil || len(skills) == 0 {
		return nil, err
	}
	var prompt strings.Builder
	prompt.WriteString("Available project skills. When a skill applies, call activate_skill with its exact name before using it. The tool returns its full instructions and source path.\n")
	for _, item := range skills {
		fmt.Fprintf(&prompt, "- %s: %s (source: %s)\n", item.Name, item.Description, item.Path)
	}
	return []*schema.Message{schema.SystemMessage(prompt.String())}, nil
}

func (m *Skill) Tools(context.Context) ([]tool.BaseTool, error) {
	if m == nil || m.loader == nil {
		return nil, nil
	}
	return []tool.BaseTool{tools.NewActivateSkillTool(m.loader)}, nil
}

func (m *Skill) list(ctx context.Context) ([]*backend.SkillMetadata, error) {
	if m == nil || m.loader == nil {
		return nil, nil
	}
	items, err := m.loader.ListSkills(ctx)
	if err != nil {
		return nil, err
	}
	items = append([]*backend.SkillMetadata(nil), items...)
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	return items, nil
}
