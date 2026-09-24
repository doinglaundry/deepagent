package tools

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type DeleteFileTool struct{ workspace backend.WorkspaceBackend }

func (*DeleteFileTool) RequiresApproval() bool { return true }

func NewDeleteFileTool(workspace backend.WorkspaceBackend) tool.BaseTool {
	return &DeleteFileTool{workspace: workspace}
}
func (*DeleteFileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("delete_file", "Delete a file in the workspace.", map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}})
}
func (t *DeleteFileTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeToolArgs(args, &in); err != nil {
		return "", err
	}
	return t.workspace.DeleteFile(ctx, in.Path)
}
