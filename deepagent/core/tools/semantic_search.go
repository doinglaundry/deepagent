package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"eino-cli/deepagent/core/backend"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type semanticSearchArgs struct {
	Query string `json:"query" jsonschema:"required,description=Natural-language question about code"`
	Path  string `json:"path,omitempty" jsonschema:"description=Optional file or directory to search"`
}

type semanticMatch struct {
	path, text  string
	line, score int
}

// NewSemanticSearchTool provides a local, deterministic semantic-like search.
// It ranks files and lines by query-term matches without requiring an index.
func NewSemanticSearchTool(workspace backend.Filesystem) (tool.BaseTool, error) {
	if workspace == nil {
		return nil, fmt.Errorf("workspace is required")
	}
	return &semanticSearchTool{workspace: workspace}, nil
}

type semanticSearchTool struct{ workspace backend.Filesystem }

func (*semanticSearchTool) ReadOnly() bool     { return true }
func (*semanticSearchTool) ParallelSafe() bool { return true }
func (*semanticSearchTool) Info(context.Context) (*schema.ToolInfo, error) {
	return toolInfo("semantic_search", "Rank code paths and lines by query-term matches.", map[string]*schema.ParameterInfo{"query": {Type: schema.String, Required: true}, "path": {Type: schema.String}})
}
func (t *semanticSearchTool) InvokableRun(ctx context.Context, raw string, _ ...tool.Option) (string, error) {
	var input semanticSearchArgs
	err := json.Unmarshal([]byte(raw), &input)
	if err != nil {
		return "", err
	}
	terms := semanticTerms(input.Query)
	if len(terms) == 0 {
		return "", fmt.Errorf("query must include searchable terms")
	}
	var matches []semanticMatch
	read := func(path string) error {
		limit := int(^uint(0) >> 1)
		content, err := t.workspace.Read(ctx, path, nil, &limit)
		if err != nil {
			return err
		}
		if bytesBinary([]byte(content)) {
			return nil
		}
		pathScore := scoreTerms(strings.ToLower(path), terms) * 3
		for i, line := range strings.Split(content, "\n") {
			score := scoreTerms(strings.ToLower(line), terms) + pathScore
			if score > 0 {
				matches = append(matches, semanticMatch{path: path, line: i + 1, score: score, text: strings.TrimSpace(line)})
			}
		}
		return nil
	}
	pending := []string{input.Path}
	for len(pending) > 0 {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		path := pending[0]
		pending = pending[1:]
		entries, err := t.workspace.List(ctx, path)
		if err != nil {
			if err := read(path); err != nil {
				return "", err
			}
			continue
		}
		for _, entry := range entries {
			if entry.IsSymlink {
				continue
			}
			if entry.IsDir {
				switch entry.Name() {
				case ".git", "node_modules", "vendor", ".cache":
					continue
				}
				pending = append(pending, entry.Path)
			} else if err := read(entry.Path); err != nil {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				// Unreadable or oversized files do not prevent searching other files.
			}
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if matches[i].path != matches[j].path {
			return matches[i].path < matches[j].path
		}
		return matches[i].line < matches[j].line
	})
	if len(matches) > 10 {
		matches = matches[:10]
	}
	if len(matches) == 0 {
		return "No semantic matches found", nil
	}
	lines := make([]string, len(matches))
	for i, m := range matches {
		lines[i] = fmt.Sprintf("%s:%d: %s", m.path, m.line, m.text)
	}
	return strings.Join(lines, "\n"), nil
}

func semanticTerms(query string) []string {
	seen := map[string]bool{}
	var result []string
	for _, term := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }) {
		if len(term) >= 3 && !seen[term] {
			seen[term] = true
			result = append(result, term)
		}
	}
	return result
}

func scoreTerms(text string, terms []string) int {
	score := 0
	for _, term := range terms {
		if strings.Contains(text, term) {
			score++
		}
	}
	return score
}

func bytesBinary(data []byte) bool {
	limit := len(data)
	if limit > 8000 {
		limit = 8000
	}
	return bytesIndexByte(data[:limit], 0)
}

func bytesIndexByte(data []byte, target byte) bool {
	for _, b := range data {
		if b == target {
			return true
		}
	}
	return false
}
