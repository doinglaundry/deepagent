package tools

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	skillspkg "eino-cli/deepagent/graph/skills"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type ActivateSkillTool struct{ loader skillspkg.SkillLoader }

func (*ActivateSkillTool) ReadOnly() bool { return true }

func (t *ActivateSkillTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	items, err := t.loader.ListSkills(ctx)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item != nil && strings.TrimSpace(item.Name) != "" {
			names = append(names, item.Name)
		}
	}
	sort.Strings(names)
	return &schema.ToolInfo{Name: "activate_skill", Desc: "Load full instructions for one available project skill.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"name": {Type: schema.String, Required: true, Enum: names},
	})}, nil
}

func NewActivateSkillTool(loader skillspkg.SkillLoader) *ActivateSkillTool {
	return &ActivateSkillTool{loader: loader}
}

func (t *ActivateSkillTool) InvokableRun(ctx context.Context, raw string, _ ...tool.Option) (string, error) {
	var input struct {
		Name string `json:"name"`
	}
	err := json.Unmarshal([]byte(raw), &input)
	if err != nil {
		return "", err
	}
	return skillspkg.LoadSkillContent(ctx, t.loader, input.Name)
}
