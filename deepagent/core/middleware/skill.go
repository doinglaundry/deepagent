package middleware

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/core/tools"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type SkillMiddleware struct {
	BaseMiddleware
	loader backend.SkillLoader
}

func NewSkillMiddleware(loader backend.SkillLoader) Middleware {
	return &SkillMiddleware{loader: loader}
}
func (m *SkillMiddleware) Name() string { return "skill" }

func (m *SkillMiddleware) BuildPrompt(ctx context.Context) ([]*schema.Message, error) {

	if m == nil || m.loader == nil {
		return nil, nil
	}

	skillMetaList, err := m.loader.ListSkills(ctx)
	if err != nil {
		return nil, err
	}
	skillMetaList = append([]*backend.SkillMetadata(nil), skillMetaList...)

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
