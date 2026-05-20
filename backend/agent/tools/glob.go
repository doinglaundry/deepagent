package tools

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"eino-cli/backend/consts"
	"eino-cli/backend/sandbox"
)

type globArgs struct {
	Pattern string `json:"pattern" jsonschema:"description=The glob pattern to match files against"`
	// Description copied verbatim from eino — part of the trained-on prompt.
	Path string `json:"path" jsonschema:"description=The directory to search in. If not specified\\, the current working directory will be used. IMPORTANT: Omit this field to use the default directory. DO NOT enter 'undefined' or 'null' - simply omit it for the default behavior. Must be a valid directory path if provided."`
}

// GetGlobTool returns the glob tool; results are absolute paths.
func GetGlobTool(sandboxManager sandbox.SandboxManager) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameGlob, filesystem.GlobToolDesc,
		func(ctx context.Context, in globArgs) (string, error) {
			if shouldUseSandbox(in.Path) {
				if sb := getSandbox(ctx, sandboxManager); sb != nil {
					matches, _, err := sb.Glob(ctx, in.Path, normalizeGlobPattern(in.Pattern), sandbox.GlobOpts{})
					if err == nil {
						if len(matches) == 0 {
							return consts.NoFilesFound, nil
						}
						sort.Strings(matches)
						return strings.Join(matches, "\n"), nil
					}
				}
			}
			searchBase := resolveRoot()
			if in.Path != "" {
				var err error
				searchBase, err = getResolvedPath(in.Path)
				if err != nil {
					return "", err
				}
			}
			pattern := normalizeGlobPattern(in.Pattern)
			paths, err := doublestar.FilepathGlob(filepath.Join(searchBase, pattern))
			if err != nil {
				return "", err
			}
			absolutePaths := make([]string, 0, len(paths))
			for _, p := range paths {
				info, statErr := os.Stat(p)
				if statErr != nil || info.IsDir() {
					continue
				}
				abs, absErr := filepath.Abs(p)
				if absErr != nil {
					abs = p
				}
				absolutePaths = append(absolutePaths, abs)
			}
			sort.Strings(absolutePaths)
			if len(absolutePaths) == 0 {
				return consts.NoFilesFound, nil
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
