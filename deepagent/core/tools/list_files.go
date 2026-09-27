package tools

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type ListFilesTool struct {
	backend backend.Filesystem
}

func NewListFilesTool(backend backend.Filesystem) tool.BaseTool {
	return &ListFilesTool{backend: backend}
}
func (*ListFilesTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("list_files", "List files in a directory.", map[string]*schema.ParameterInfo{"path": {Type: schema.String}})
}
func (*ListFilesTool) ReadOnly() bool     { return true }
func (*ListFilesTool) ParallelSafe() bool { return true }
func (t *ListFilesTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var in struct {
		Path string `json:"path"`
	}
	err := json.Unmarshal([]byte(args), &in)
	if err != nil {
		return "", err
	}
	items, err := t.backend.List(ctx, in.Path)
	if err != nil {
		return "", err
	}
	return fmt.Sprint(items), nil
}
