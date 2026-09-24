package tools

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type ListFilesTool struct {
	backend backend.Backend
	name    string
}

func NewListFilesTool(backend backend.Backend) tool.BaseTool {
	return &ListFilesTool{backend: backend}
}
func (t *ListFilesTool) Info(context.Context) (*schema.ToolInfo, error) {
	name := t.name
	if name == "" {
		name = "list_files"
	}
	return toolInfo(name, "List files in a directory.", map[string]*schema.ParameterInfo{"path": {Type: schema.String}})
}
func (*ListFilesTool) ReadOnly() bool     { return true }
func (*ListFilesTool) ParallelSafe() bool { return true }
func (t *ListFilesTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeToolArgs(args, &in); err != nil {
		return "", err
	}
	items, err := t.backend.LsInfo(ctx, in.Path)
	if err != nil {
		return "", err
	}
	return fmt.Sprint(items), nil
}
