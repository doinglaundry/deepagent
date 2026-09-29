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

type EditFileTool struct{ backend backend.Filesystem }

func (*EditFileTool) RequiresApproval() bool { return true }

func NewEditFileTool(backend backend.Filesystem) tool.BaseTool {
	return &EditFileTool{backend: backend}
}
func (*EditFileTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("edit_file", "Replace an exact text span in a file; set replace_all to replace every occurrence.", map[string]*schema.ParameterInfo{"path": {Type: schema.String, Required: true}, "old": {Type: schema.String, Required: true}, "new": {Type: schema.String, Required: true}, "replace_all": {Type: schema.Boolean}})
}
func (t *EditFileTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var in struct {
		Path       string  `json:"path"`
		Old        string  `json:"old"`
		New        *string `json:"new"`
		ReplaceAll bool    `json:"replace_all"`
	}
	err := json.Unmarshal([]byte(args), &in)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(in.Path) == "" {
		return "", errors.New("path is required")
	}
	if in.New == nil {
		return "", errors.New("new is required")
	}
	result, err := t.backend.Edit(ctx, in.Path, in.Old, *in.New, in.ReplaceAll)
	if err != nil {
		return "", err
	}
	if result != nil && result.Error != "" {
		return "", result.Error
	}
	return "edited " + in.Path, nil
}
