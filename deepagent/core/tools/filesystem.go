package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"eino-cli/deepagent/core/backend"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type fileSearchTool struct {
	backend backend.Filesystem
	name    string
}

func (*fileSearchTool) ReadOnly() bool     { return true }
func (*fileSearchTool) ParallelSafe() bool { return true }
func (t *fileSearchTool) Info(context.Context) (*schema.ToolInfo, error) {
	params := map[string]*schema.ParameterInfo{"pattern": {Type: schema.String, Required: t.name == "glob"}, "path": {Type: schema.String}, "glob": {Type: schema.String}, "ignore_case": {Type: schema.Boolean}, "head_limit": {Type: schema.Integer}}
	description := "Find workspace paths matching a glob pattern."
	if t.name != "glob" {
		params["query"] = &schema.ParameterInfo{Type: schema.String}
		description = "Search workspace text. pattern is a regular expression; query matches literal text."
	}
	return toolInfo(t.name, description, params)
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
	err := json.Unmarshal([]byte(raw), &input)
	if err != nil {
		return "", err
	}
	if input.Pattern == "" {
		input.Pattern = input.Query
		if t.name != "glob" {
			input.Pattern = regexp.QuoteMeta(input.Query)
			if input.Query != "" && input.HeadLimit <= 0 {
				input.HeadLimit = 100
			}
		}
	}
	if input.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if t.name == "glob" {
		files, err := t.backend.Glob(ctx, input.Pattern, input.Path)
		if err != nil {
			return "", err
		}
		data, err := json.Marshal(files)
		return string(data), err
	}
	if input.IgnoreCase {
		input.Pattern = "(?i)" + input.Pattern
	}
	matches, err := t.backend.Grep(ctx, input.Pattern, input.Path, input.Glob)
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
