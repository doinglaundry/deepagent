package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"unicode"

	"eino-cli/deepagent/core/backend"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type semanticSearchArgs struct {
	Query string `json:"query"`
	Path  string `json:"path"`
}

type semanticMatch struct {
	path, text  string
	line, score int
}

// NewSemanticSearchTool provides a local, deterministic semantic-like search.
// It ranks files and lines by query-term matches without requiring an index.
func NewSemanticSearchTool(filesystem backend.Filesystem) (tool.BaseTool, error) {
	if filesystem == nil {
		return nil, fmt.Errorf("filesystem is required")
	}
	return &semanticSearchTool{filesystem: filesystem}, nil
}

type semanticSearchTool struct{ filesystem backend.Filesystem }

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
	matches, err := t.findMatches(ctx, input.Path, terms)
	if err != nil {
		return "", err
	}
	slices.SortStableFunc(matches, func(a, b semanticMatch) int {
		return cmp.Or(cmp.Compare(b.score, a.score), cmp.Compare(a.path, b.path), cmp.Compare(a.line, b.line))
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

func (t *semanticSearchTool) findMatches(ctx context.Context, start string, terms []string) ([]semanticMatch, error) {
	type fileToRead struct {
		path     string
		required bool
	}
	pending := []string{start}
	var files []fileToRead
	for len(pending) > 0 {
		contextErr := ctx.Err()
		if contextErr != nil {
			return nil, contextErr
		}
		path := pending[0]
		pending = pending[1:]
		entries, err := t.filesystem.List(ctx, path)
		if err != nil {
			files = append(files, fileToRead{path: path, required: true})
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
				continue
			}
			files = append(files, fileToRead{path: entry.Path})
		}
	}
	var matches []semanticMatch
	limit := math.MaxInt
	for _, file := range files {
		content, err := t.filesystem.Read(ctx, file.path, nil, &limit)
		if err != nil {
			if file.required {
				return nil, err
			}
			contextErr := ctx.Err()
			if contextErr != nil {
				return nil, contextErr
			}
			continue
		}
		if strings.IndexByte(content[:min(len(content), 8000)], 0) >= 0 {
			continue
		}
		pathScore := scoreTerms(strings.ToLower(file.path), terms) * 3
		for i, line := range strings.Split(content, "\n") {
			score := scoreTerms(strings.ToLower(line), terms) + pathScore
			if score > 0 {
				matches = append(matches, semanticMatch{path: file.path, line: i + 1, score: score, text: strings.TrimSpace(line)})
			}
		}
	}
	return matches, nil
}

func semanticTerms(query string) []string {
	var result []string
	for _, term := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' }) {
		if len(term) < 3 || slices.Contains(result, term) {
			continue
		}
		result = append(result, term)
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
