package tools

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"strings"
)

type WriteFileTool struct{ backend backend.Filesystem }

func (*WriteFileTool) RequiresApproval() bool { return true }

func NewWriteFileTool(backend backend.Filesystem) tool.BaseTool {
	return &WriteFileTool{backend: backend}
}
func (*WriteFileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("write_file", "Write a UTF-8 file.", map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}, "content": {Type: schema.String, Required: true}})
}
func (t *WriteFileTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var in struct {
		Path    string  `json:"path"`
		Content *string `json:"content"`
	}
	err := json.Unmarshal([]byte(args), &in)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", errors.New("path is required")
	}
	if in.Content == nil {
		return "", errors.New("content is required")
	}
	result, err := t.backend.Write(ctx, in.Path, *in.Content)
	if err != nil {
		return "", err
	}
	if result != nil && result.Error != "" {
		return "", result.Error
	}
	return "wrote " + in.Path, nil
}
