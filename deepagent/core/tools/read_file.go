package tools

import (
	"context"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func NewReadFileTool(backend interface {
	Read(context.Context, string, *int, *int) (string, error)
}) tool.BaseTool {
	return &readFileTool{backend: backend}
}

type readFileTool struct {
	backend interface {
		Read(context.Context, string, *int, *int) (string, error)
	}
}

func (*readFileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("read_file", "Read a UTF-8 file. Offset is a one-based line number.", map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}, "offset": {Type: schema.Integer}, "limit": {Type: schema.Integer}})
}
func (*readFileTool) ReadOnly() bool     { return true }
func (*readFileTool) ParallelSafe() bool { return true }
func (t *readFileTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var in struct {
		Path   string `json:"path"`
		Offset *int   `json:"offset"`
		Limit  *int   `json:"limit"`
	}
	if err := decodeToolArgs(args, &in); err != nil {
		return "", err
	}
	if in.Offset != nil && *in.Offset > 0 {
		*in.Offset--
	}
	return t.backend.Read(ctx, in.Path, in.Offset, in.Limit)
}
