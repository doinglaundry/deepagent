package tools

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"regexp"
	"strings"
)

type SearchFilesTool struct{ backend backend.Filesystem }

func NewSearchFilesTool(backend backend.Filesystem) tool.BaseTool {
	return &SearchFilesTool{backend: backend}
}
func (*SearchFilesTool) ReadOnly() bool     { return true }
func (*SearchFilesTool) ParallelSafe() bool { return true }
func (*SearchFilesTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("search_files", "Search literal text in workspace files (at most 100 matches).", map[string]*schema.ParameterInfo{"query": {Type: schema.String, Required: true}, "path": {Type: schema.String}})
}
func (t *SearchFilesTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	var in struct {
		Query string `json:"query"`
		Path  string `json:"path"`
	}
	if err := decodeToolArgs(args, &in); err != nil {
		return "", err
	}
	if in.Query == "" {
		return "", fmt.Errorf("query required")
	}
	matches, err := t.backend.Grep(ctx, regexp.QuoteMeta(in.Query), in.Path, "")
	if err != nil {
		return "", err
	}
	if len(matches) > 100 {
		matches = matches[:100]
	}
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = fmt.Sprintf("%s:%d:%s", m.Path, m.Line, m.Text)
	}
	return strings.Join(out, "\n"), nil
}
