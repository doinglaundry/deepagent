package tools

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const semanticSearchToolDesc = `Find code by meaning using a local heuristic search. This CLI version does not use Cursor's private semantic index; it ranks files by query term matches in paths and content.`

type semanticSearchArgs struct {
	Query string `json:"query" jsonschema:"required,description=Natural-language question about code"`
	Path  string `json:"path,omitempty" jsonschema:"description=Optional file or directory to search"`
}

type semanticMatch struct {
	path  string
	line  int
	score int
	text  string
}

func GetSemanticSearchTool() (tool.BaseTool, error) {
	return utils.InferTool("semantic_search", semanticSearchToolDesc,
		func(ctx context.Context, in semanticSearchArgs) (string, error) {
			return semanticSearch(resolveRoot(), in)
		})
}

func semanticSearch(root string, in semanticSearchArgs) (string, error) {
	terms := getSemanticTerms(in.Query)
	if len(terms) == 0 {
		return "", fmt.Errorf("query must include searchable terms")
	}
	searchPath := resolveRoot()
	if strings.TrimSpace(in.Path) != "" {
		var err error
		searchPath, err = getResolvedPath(in.Path)
		if err != nil {
			return "", err
		}
	}
	var matches []semanticMatch
	walkErr := filepath.WalkDir(searchPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil || isLikelyBinary(data) {
			return nil
		}
		scorePath := scoreText(strings.ToLower(path), terms) * 3
		for i, line := range strings.Split(string(data), "\n") {
			score := scoreText(strings.ToLower(line), terms) + scorePath
			if score == 0 {
				continue
			}
			matches = append(matches, semanticMatch{path: path, line: i + 1, score: score, text: strings.TrimSpace(line)})
		}
		return nil
	})
	if walkErr != nil {
		return "", walkErr
	}
	if len(matches) == 0 {
		return "No semantic matches found", nil
	}
	sort.SliceStable(matches, func(i, j int) bool {
		if matches[i].score == matches[j].score {
			if matches[i].path == matches[j].path {
				return matches[i].line < matches[j].line
			}
			return matches[i].path < matches[j].path
		}
		return matches[i].score > matches[j].score
	})
	if len(matches) > 10 {
		matches = matches[:10]
	}
	lines := make([]string, len(matches))
	for i, match := range matches {
		lines[i] = fmt.Sprintf("%s:%d: %s", match.path, match.line, match.text)
	}
	return strings.Join(lines, "\n"), nil
}

func getSemanticTerms(query string) []string {
	seen := map[string]bool{}
	var terms []string
	for _, field := range strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
		if len(field) < 3 || seen[field] {
			continue
		}
		seen[field] = true
		terms = append(terms, field)
	}
	return terms
}

func scoreText(text string, terms []string) int {
	score := 0
	for _, term := range terms {
		if strings.Contains(text, term) {
			score++
		}
	}
	return score
}

func isLikelyBinary(data []byte) bool {
	limit := min(len(data), 8000)
	for _, b := range data[:limit] {
		if b == 0 {
			return true
		}
	}
	return false
}
