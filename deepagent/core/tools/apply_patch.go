package tools

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func NewApplyPatchTool(patcher backend.Filesystem) tool.BaseTool {
	return &applyPatchTool{backend: patcher}
}

type applyPatchTool struct{ backend backend.Filesystem }

func (*applyPatchTool) RequiresApproval() bool { return true }

func (*applyPatchTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "apply_patch",
		Desc: "Apply a file-oriented patch inside the workspace.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"patch": {Type: schema.String, Required: true},
		}),
	}, nil
}

func (t *applyPatchTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var input struct {
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal([]byte(arguments), &input); err != nil {
		return "", err
	}
	if input.Patch == "" {
		return "", fmt.Errorf("patch is required")
	}
	return t.backend.ApplyPatch(ctx, input.Patch)
}
