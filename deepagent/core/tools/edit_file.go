package tools

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"encoding/json"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type EditFileTool struct{ backend backend.Filesystem }

func (*EditFileTool) RequiresApproval() bool { return true }

func NewEditFileTool(backend backend.Filesystem) tool.BaseTool {
	return &EditFileTool{backend: backend}
}
func (*EditFileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("edit_file", "Replace one text span in a file.", map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}, "old": {Type: schema.String, Required: true}, "new": {Type: schema.String, Required: true}})
}
func (t *EditFileTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var in struct {
		Path       string `json:"path"`
		Old        string `json:"old"`
		New        string `json:"new"`
		ReplaceAll bool   `json:"replace_all"`
	}
	err := json.Unmarshal([]byte(args), &in)
	if err != nil {
		return "", err
	}
	result, err := t.backend.Edit(ctx, in.Path, in.Old, in.New, in.ReplaceAll)
	if err != nil {
		return "", err
	}
	if result != nil && result.Error != "" {
		return "", result.Error
	}
	return "edited " + in.Path, nil
}
