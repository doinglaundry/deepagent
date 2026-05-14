package tools

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const rgToolDesc = `Search file contents with ripgrep. Supports content, files_with_matches, and count output modes. Returns "No matches found" when ripgrep finds nothing.`

type rgArgs struct {
	Pattern         string `json:"pattern" jsonschema:"required,description=Regular expression to search for"`
	Path            string `json:"path,omitempty" jsonschema:"description=File or directory to search; omit for workspace root"`
	Glob            string `json:"glob,omitempty" jsonschema:"description=Optional glob filter"`
	OutputMode      string `json:"output_mode,omitempty" jsonschema:"description=content, files_with_matches, or count"`
	BeforeLines     int    `json:"-B,omitempty" jsonschema:"description=Lines before each match"`
	AfterLines      int    `json:"-A,omitempty" jsonschema:"description=Lines after each match"`
	Context         int    `json:"-C,omitempty" jsonschema:"description=Lines before and after each match"`
	IgnoreCase      bool   `json:"-i,omitempty" jsonschema:"description=Case-insensitive search"`
	FileType        string `json:"type,omitempty" jsonschema:"description=Ripgrep file type filter"`
	HeadLimit       int    `json:"head_limit,omitempty" jsonschema:"description=Maximum output lines to return"`
	Offset          int    `json:"offset,omitempty" jsonschema:"description=Skip first N output lines"`
	Multiline       bool   `json:"multiline,omitempty" jsonschema:"description=Enable multiline search"`
	ShowLineNumbers *bool  `json:"-n,omitempty" jsonschema:"description=Show line numbers in content output"`
}

func GetRgTool(root string) (tool.BaseTool, error) {
	return utils.InferTool("rg", rgToolDesc,
		func(ctx context.Context, in rgArgs) (string, error) {
			return runRipgrep(ctx, root, in)
		})
}

func runRipgrep(ctx context.Context, root string, in rgArgs) (string, error) {
	if strings.TrimSpace(in.Pattern) == "" {
		return "", fmt.Errorf("pattern must not be empty")
	}
	searchPath := resolveRoot(root)
	if strings.TrimSpace(in.Path) != "" {
		var err error
		searchPath, err = getResolvedPath(root, in.Path)
		if err != nil {
			return "", err
		}
	}
	args := []string{"--color", "never"}
	switch in.OutputMode {
	case "", "content":
		if in.ShowLineNumbers == nil || *in.ShowLineNumbers {
			args = append(args, "--line-number")
		}
	case "files_with_matches":
		args = append(args, "--files-with-matches")
	case "count":
		args = append(args, "--count-matches")
	default:
		return "", fmt.Errorf("unsupported output_mode: %s", in.OutputMode)
	}
	if in.Context > 0 {
		args = append(args, "-C", fmt.Sprint(in.Context))
	}
	if in.BeforeLines > 0 {
		args = append(args, "-B", fmt.Sprint(in.BeforeLines))
	}
	if in.AfterLines > 0 {
		args = append(args, "-A", fmt.Sprint(in.AfterLines))
	}
	if in.IgnoreCase {
		args = append(args, "-i")
	}
	if in.Glob != "" {
		args = append(args, "--glob", in.Glob)
	}
	if in.FileType != "" {
		args = append(args, "--type", in.FileType)
	}
	if in.Multiline {
		args = append(args, "-U", "--multiline-dotall")
	}
	args = append(args, in.Pattern, searchPath)

	cmd := exec.CommandContext(ctx, "rg", args...)
	cmd.Dir = resolveRoot(root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return runLocalRipgrepFallback(root, in)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return noMatchesFound, nil
		}
		return "", err
	}
	output := strings.TrimRight(string(out), "\n")
	if output == "" {
		return noMatchesFound, nil
	}
	return paginateOutput(output, in.Offset, in.HeadLimit), nil
}

func runLocalRipgrepFallback(root string, in rgArgs) (string, error) {
	grepIn := grepArgs{
		Pattern:         in.Pattern,
		OutputMode:      in.OutputMode,
		CaseInsensitive: &in.IgnoreCase,
		HeadLimit:       &in.HeadLimit,
		Offset:          &in.Offset,
	}
	if in.Path != "" {
		grepIn.Path = &in.Path
	}
	matches, err := runGrep(root, grepIn)
	if err != nil {
		return "", err
	}
	switch in.OutputMode {
	case "", "content":
		if len(matches) == 0 {
			return noMatchesFound, nil
		}
		return formatGrepContent(applyPagination(matches, in.Offset, in.HeadLimit), true), nil
	case "files_with_matches":
		if len(matches) == 0 {
			return noMatchesFound, nil
		}
		seen := map[string]bool{}
		var paths []string
		for _, match := range matches {
			if seen[match.Path] {
				continue
			}
			seen[match.Path] = true
			paths = append(paths, match.Path)
		}
		paths = applyPagination(paths, in.Offset, in.HeadLimit)
		return strings.Join(paths, "\n"), nil
	case "count":
		if len(matches) == 0 {
			return noMatchesFound, nil
		}
		return formatGrepCount(matches, in.Offset, in.HeadLimit), nil
	default:
		return "", fmt.Errorf("unsupported output_mode: %s", in.OutputMode)
	}
}

func paginateOutput(output string, offset, headLimit int) string {
	lines := strings.Split(output, "\n")
	lines = applyPagination(lines, offset, headLimit)
	return strings.Join(lines, "\n")
}
