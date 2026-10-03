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

type ActivateSkillTool struct{ skillLoader skillspkg.SkillLoader }

func (activateSkillTool *ActivateSkillTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	skillMetadata, err := activateSkillTool.skillLoader.ListSkills(ctx)
	if err != nil {
		return nil, err
	}
	skillNames := make([]string, 0, len(skillMetadata))
	for _, skill := range skillMetadata {
		if skill != nil && strings.TrimSpace(skill.Name) != "" {
			skillNames = append(skillNames, skill.Name)
		}
	}
	sort.Strings(skillNames)
	return &schema.ToolInfo{Name: "activate_skill", Desc: "Load full instructions for one available project skill.", ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"name": {Type: schema.String, Required: true, Enum: skillNames},
	})}, nil
}

func NewActivateSkillTool(skillLoader skillspkg.SkillLoader) ToolDescriptor {
	return ToolDescriptor{Tool: &ActivateSkillTool{skillLoader: skillLoader}, ReadOnly: true}
}

func (activateSkillTool *ActivateSkillTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var skillArgs struct {
		Name string `json:"name"`
	}
	err := json.Unmarshal([]byte(arguments), &skillArgs)
	if err != nil {
		return "", err
	}
	return skillspkg.LoadSkillContent(ctx, activateSkillTool.skillLoader, skillArgs.Name)
}
