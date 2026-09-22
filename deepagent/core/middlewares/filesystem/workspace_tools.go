package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"eino-cli/deepagent/core/backends"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

func newDeleteFileTool(backend backends.WorkspaceBackend) tool.BaseTool {
	t, _ := utils.InferTool("delete_file", "Delete one file inside the workspace. Directories are refused.", func(ctx context.Context, in struct {
		FilePath string `json:"file_path" jsonschema:"required"`
	}) (string, error) {
		return backend.DeleteFile(ctx, in.FilePath)
	})
	return t
}

func newWorkspaceReadTools(backend backends.WorkspaceBackend) []tool.BaseTool {
	rg, _ := utils.InferTool("rg", "Search workspace files with ripgrep.", func(ctx context.Context, in struct {
		Pattern    string `json:"pattern" jsonschema:"required"`
		Path       string `json:"path,omitempty"`
		Glob       string `json:"glob,omitempty"`
		IgnoreCase bool   `json:"ignore_case,omitempty"`
		HeadLimit  int    `json:"head_limit,omitempty"`
	}) (string, error) {
		if strings.TrimSpace(in.Pattern) == "" {
			return "", errors.New("pattern is required")
		}
		path, err := workspacePath(backend.RootDir(), in.Path)
		if err != nil {
			return "", err
		}
		args := []string{"--color", "never", "--line-number"}
		if in.IgnoreCase {
			args = append(args, "-i")
		}
		if in.Glob != "" {
			args = append(args, "--glob", in.Glob)
		}
		args = append(args, in.Pattern, path)
		out, runErr := exec.CommandContext(ctx, "rg", args...).CombinedOutput()
		if runErr != nil {
			var exitErr *exec.ExitError
			if errors.As(runErr, &exitErr) && exitErr.ExitCode() == 1 {
				return "No matches found", nil
			}
			return "", runErr
		}
		return limitLines(strings.TrimRight(string(out), "\n"), in.HeadLimit), nil
	})

	semantic, _ := utils.InferTool("semantic_search", "Find code by meaning using local term ranking.", func(_ context.Context, in struct {
		Query string `json:"query" jsonschema:"required"`
		Path  string `json:"path,omitempty"`
	}) (string, error) {
		return semanticSearch(backend.RootDir(), in.Path, in.Query)
	})

	lints, _ := utils.InferTool("read_lints", "Run Go diagnostics for workspace packages.", func(ctx context.Context, in struct {
		Paths []string `json:"paths,omitempty"`
	}) (string, error) {
		args := []string{"test"}
		if len(in.Paths) == 0 {
			args = append(args, "./...")
		} else {
			for _, raw := range in.Paths {
				path, err := workspacePath(backend.RootDir(), raw)
				if err != nil {
					return "", err
				}
				info, err := os.Stat(path)
				if err != nil {
					return "", err
				}
				if !info.IsDir() {
					if filepath.Ext(path) != ".go" {
						continue
					}
					path = filepath.Dir(path)
				}
				rel, _ := filepath.Rel(backend.RootDir(), path)
				args = append(args, "./"+filepath.ToSlash(rel)+"/...")
			}
		}
		if len(args) == 1 {
			return "No diagnostics provider for the selected paths", nil
		}
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = backend.RootDir()
		out, err := cmd.CombinedOutput()
		if err != nil {
			return limitBytes(strings.TrimRight(string(out), "\n"), 64*1024), nil
		}
		return "No diagnostics", nil
	})
	return []tool.BaseTool{rg, semantic, lints}
}

func workspacePath(root, raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return root, nil
	}
	path := raw
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	path = filepath.Clean(path)
	rel, err := filepath.Rel(filepath.Clean(root), path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", backends.ErrInvalidPath
	}
	return path, nil
}

type semanticMatch struct {
	path  string
	line  int
	score int
	text  string
}

func semanticSearch(root, rawPath, query string) (string, error) {
	terms := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	})
	if len(terms) == 0 {
		return "", errors.New("query is required")
	}
	path, err := workspacePath(root, rawPath)
	if err != nil {
		return "", err
	}
	var matches []semanticMatch
	err = filepath.WalkDir(path, func(name string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(name)
		if readErr != nil || strings.IndexByte(string(data[:min(len(data), 8000)]), 0) >= 0 {
			return nil
		}
		rel, _ := filepath.Rel(root, name)
		pathScore := scoreTerms(strings.ToLower(rel), terms) * 3
		for line, text := range strings.Split(string(data), "\n") {
			score := pathScore + scoreTerms(strings.ToLower(text), terms)
			if score > 0 {
				matches = append(matches, semanticMatch{rel, line + 1, score, strings.TrimSpace(text)})
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].score > matches[j].score })
	if len(matches) > 10 {
		matches = matches[:10]
	}
	if len(matches) == 0 {
		return "No semantic matches found", nil
	}
	lines := make([]string, len(matches))
	for i, match := range matches {
		lines[i] = fmt.Sprintf("%s:%d: %s", filepath.ToSlash(match.path), match.line, match.text)
	}
	return strings.Join(lines, "\n"), nil
}

func scoreTerms(text string, terms []string) int {
	score := 0
	for _, term := range terms {
		if len(term) >= 3 && strings.Contains(text, term) {
			score++
		}
	}
	return score
}

func limitLines(value string, limit int) string {
	if limit <= 0 {
		limit = 200
	}
	lines := strings.Split(value, "\n")
	if len(lines) > limit {
		lines = lines[:limit]
	}
	return strings.Join(lines, "\n")
}

func limitBytes(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
