package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode"

	filesystempkg "eino-cli/deepagent/graph/filesystem"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type fileSearchTool struct {
	filesystem filesystempkg.Filesystem
	toolName   string
}

func newFileSearchTool(filesystem filesystempkg.Filesystem, toolName string) ToolDescriptor {
	return ToolDescriptor{Tool: &fileSearchTool{filesystem: filesystem, toolName: toolName}, ReadOnly: true, ParallelSafe: true}
}

func (fileSearchTool *fileSearchTool) Info(context.Context) (*schema.ToolInfo, error) {
	parameters := map[string]*schema.ParameterInfo{"pattern": {Type: schema.String, Required: fileSearchTool.toolName == "glob"}, "path": {Type: schema.String}, "glob": {Type: schema.String}, "ignore_case": {Type: schema.Boolean}, "head_limit": {Type: schema.Integer}}
	description := "Find workspace paths matching a glob pattern."
	if fileSearchTool.toolName != "glob" {
		parameters["query"] = &schema.ParameterInfo{Type: schema.String}
		description = "Search workspace text. pattern is a regular expression; query matches literal text."
	}
	return newToolInfo(fileSearchTool.toolName, description, parameters)
}

func (fileSearchTool *fileSearchTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var searchArgs struct {
		Pattern    string `json:"pattern"`
		Query      string `json:"query"`
		Path       string `json:"path"`
		Glob       string `json:"glob"`
		IgnoreCase bool   `json:"ignore_case"`
		HeadLimit  int    `json:"head_limit"`
	}
	err := json.Unmarshal([]byte(arguments), &searchArgs)
	if err != nil {
		return "", err
	}
	if searchArgs.Pattern == "" {
		searchArgs.Pattern = searchArgs.Query
		if fileSearchTool.toolName != "glob" {
			searchArgs.Pattern = regexp.QuoteMeta(searchArgs.Query)
			if searchArgs.Query != "" && searchArgs.HeadLimit <= 0 {
				searchArgs.HeadLimit = 100
			}
		}
	}
	if searchArgs.Pattern == "" {
		return "", fmt.Errorf("pattern is required")
	}
	if fileSearchTool.toolName == "glob" {
		files, err := fileSearchTool.filesystem.Glob(ctx, searchArgs.Pattern, searchArgs.Path)
		if err != nil {
			return "", err
		}
		encodedFiles, err := json.Marshal(files)
		return string(encodedFiles), err
	}
	if searchArgs.IgnoreCase {
		searchArgs.Pattern = "(?i)" + searchArgs.Pattern
	}
	matches, err := fileSearchTool.filesystem.Grep(ctx, searchArgs.Pattern, searchArgs.Path, searchArgs.Glob)
	if err != nil {
		return "", err
	}
	if searchArgs.HeadLimit > 0 && len(matches) > searchArgs.HeadLimit {
		matches = matches[:searchArgs.HeadLimit]
	}
	lines := make([]string, len(matches))
	for i, match := range matches {
		lines[i] = fmt.Sprintf("%s:%d:%s", match.Path, match.Line, match.Text)
	}
	return strings.Join(lines, "\n"), nil
}

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
func NewSemanticSearchTool(filesystem filesystempkg.Filesystem) (ToolDescriptor, error) {
	if filesystem == nil {
		return ToolDescriptor{}, fmt.Errorf("filesystem is required")
	}
	return ToolDescriptor{Tool: &semanticSearchTool{filesystem: filesystem}, ReadOnly: true, ParallelSafe: true}, nil
}

type semanticSearchTool struct{ filesystem filesystempkg.Filesystem }

func (*semanticSearchTool) Info(context.Context) (*schema.ToolInfo, error) {
	return newToolInfo("semantic_search", "Rank code paths and lines by query-term matches.", map[string]*schema.ParameterInfo{"query": {Type: schema.String, Required: true}, "path": {Type: schema.String}})
}

func (semanticSearchTool *semanticSearchTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	var searchArgs semanticSearchArgs
	err := json.Unmarshal([]byte(arguments), &searchArgs)
	if err != nil {
		return "", err
	}
	queryTerms := extractSemanticTerms(searchArgs.Query)
	if len(queryTerms) == 0 {
		return "", fmt.Errorf("query must include searchable terms")
	}
	matches, err := semanticSearchTool.findMatches(ctx, searchArgs.Path, queryTerms)
	if err != nil {
		return "", err
	}
	slices.SortStableFunc(matches, func(firstMatch, secondMatch semanticMatch) int {
		return cmp.Or(cmp.Compare(secondMatch.score, firstMatch.score), cmp.Compare(firstMatch.path, secondMatch.path), cmp.Compare(firstMatch.line, secondMatch.line))
	})
	if len(matches) > 10 {
		matches = matches[:10]
	}
	if len(matches) == 0 {
		return "No semantic matches found", nil
	}
	lines := make([]string, len(matches))
	for i, match := range matches {
		lines[i] = fmt.Sprintf("%s:%d: %s", match.path, match.line, match.text)
	}
	return strings.Join(lines, "\n"), nil
}

func (semanticSearchTool *semanticSearchTool) findMatches(ctx context.Context, startPath string, terms []string) ([]semanticMatch, error) {
	type fileToRead struct {
		path     string
		required bool
	}
	pendingPaths := []string{startPath}
	var filesToRead []fileToRead
	for len(pendingPaths) > 0 {
		contextErr := ctx.Err()
		if contextErr != nil {
			return nil, contextErr
		}
		path := pendingPaths[0]
		pendingPaths = pendingPaths[1:]
		entries, err := semanticSearchTool.filesystem.List(ctx, path)
		if err != nil {
			filesToRead = append(filesToRead, fileToRead{path: path, required: true})
			continue
		}
		for _, entry := range entries {
			if entry.IsSymlink {
				continue
			}
			if entry.IsDir {
				switch entry.GetName() {
				case ".git", "node_modules", "vendor", ".cache":
					continue
				}
				pendingPaths = append(pendingPaths, entry.Path)
				continue
			}
			filesToRead = append(filesToRead, fileToRead{path: entry.Path})
		}
	}
	var matches []semanticMatch
	lineLimit := math.MaxInt
	for _, file := range filesToRead {
		content, err := semanticSearchTool.filesystem.Read(ctx, file.path, nil, &lineLimit)
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

func extractSemanticTerms(query string) []string {
	var terms []string
	for _, term := range strings.FieldsFunc(strings.ToLower(query), func(character rune) bool {
		return !unicode.IsLetter(character) && !unicode.IsDigit(character) && character != '_'
	}) {
		if len(term) < 3 || slices.Contains(terms, term) {
			continue
		}
		terms = append(terms, term)
	}
	return terms
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
