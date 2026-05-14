package tools

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type globArgs struct {
	Pattern string `json:"pattern" jsonschema:"description=The glob pattern to match files against"`
	// Description copied verbatim from eino filesystem.go:660 — the "DO NOT
	// enter undefined/null" wording is part of the prompt the model is trained on.
	Path string `json:"path" jsonschema:"description=The directory to search in. If not specified\\, the current working directory will be used. IMPORTANT: Omit this field to use the default directory. DO NOT enter 'undefined' or 'null' - simply omit it for the default behavior. Must be a valid directory path if provided."`
}

// GetGlobTool returns the "glob" tool. Matches are reported as absolute paths
// so follow-up read/write calls don't depend on the model rejoining paths.
func GetGlobTool(root string) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameGlob, filesystem.GlobToolDesc,
		func(ctx context.Context, in globArgs) (string, error) {
			searchBase := resolveRoot(root)
			if in.Path != "" {
				searchBase = resolvePath(root, in.Path)
			}
			pattern := normalizeGlobPattern(in.Pattern)
			paths, err := doublestar.FilepathGlob(filepath.Join(searchBase, pattern))
			if err != nil {
				return "", err
			}
			if len(paths) == 0 {
				return noFilesFound, nil
			}
			absolutePaths := make([]string, 0, len(paths))
			for _, p := range paths {
				abs, absErr := filepath.Abs(p)
				if absErr != nil {
					abs = p
				}
				absolutePaths = append(absolutePaths, abs)
			}
			return strings.Join(absolutePaths, "\n"), nil
		})
}

func normalizeGlobPattern(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	if strings.HasPrefix(pattern, "**/") {
		return pattern
	}
	return filepath.Join("**", pattern)
}
