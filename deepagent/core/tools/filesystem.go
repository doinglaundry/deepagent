package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"eino-cli/deepagent/core/backend"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func NewFilesystemTools(b backend.Backend, readOnly bool) []einotool.BaseTool {
	result := []einotool.BaseTool{NewListFilesTool(b), &ListFilesTool{backend: b, name: "ls"}, NewReadFileTool(b), &fileSearchTool{backend: b, name: "glob"}, &fileSearchTool{backend: b, name: "grep"}, &fileSearchTool{backend: b, name: "rg"}}
	if !readOnly {
		result = append(result, NewWriteFileTool(b), NewEditFileTool(b))
		if workspace, ok := b.(backend.WorkspaceBackend); ok {
			result = append(result, NewDeleteFileTool(workspace))
		}
	}
	return result
}

type fileSearchTool struct {
	backend backend.Backend
	name    string
}

func (*fileSearchTool) ReadOnly() bool     { return true }
func (*fileSearchTool) ParallelSafe() bool { return true }
func (t *fileSearchTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo(t.name, "Search workspace paths or text.", map[string]*schema.ParameterInfo{"pattern": {Type: schema.String, Required: true}, "path": {Type: schema.String}, "glob": {Type: schema.String}, "ignore_case": {Type: schema.Boolean}, "head_limit": {Type: schema.Integer}})
}
func (t *fileSearchTool) InvokableRun(ctx context.Context, raw string, _ ...einotool.Option) (string, error) {
	var input struct {
		Pattern    string `json:"pattern"`
		Query      string `json:"query"`
		Path       string `json:"path"`
		Glob       string `json:"glob"`
		IgnoreCase bool   `json:"ignore_case"`
		HeadLimit  int    `json:"head_limit"`
	}
	if err := decodeToolArgs(raw, &input); err != nil {
		return "", err
	}
	if input.Pattern == "" {
		input.Pattern = input.Query
	}
	if input.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if t.name == "glob" {
		files, err := t.backend.GlobInfo(ctx, input.Pattern, input.Path)
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(files)
		return string(data), err
	}
	if input.IgnoreCase {
		input.Pattern = "(?i)" + input.Pattern
	}
	matches, err := t.backend.GrepRaw(ctx, input.Pattern, input.Path, input.Glob)
	if err != nil {
		return "", err
	}
	if input.HeadLimit > 0 && len(matches) > input.HeadLimit {
		matches = matches[:input.HeadLimit]
	}
	lines := make([]string, len(matches))
	for i, m := range matches {
		lines[i] = fmt.Sprintf("%s:%d:%s", m.Path, m.Line, m.Text)
	}
	return strings.Join(lines, "\n"), nil
}
