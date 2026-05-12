package tools

import (
	"context"
	"path/filepath"
	"strings"

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

// GetGlobTool returns the "glob" tool. Matches are reported as paths
// relative to root (not the resolved search base) so the model sees the
// same mental model regardless of what `path` it passed in.
func GetGlobTool(root string) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameGlob, filesystem.GlobToolDesc,
		func(ctx context.Context, in globArgs) (string, error) {
			base := resolveRoot(root)
			searchBase := base
			if in.Path != "" {
				searchBase = resolvePath(root, in.Path)
			}
			paths, err := filepath.Glob(filepath.Join(searchBase, in.Pattern))
			if err != nil {
				return "", err
			}
			if len(paths) == 0 {
				return noFilesFound, nil
			}
			rels := make([]string, 0, len(paths))
			for _, p := range paths {
				rel, relErr := filepath.Rel(base, p)
				if relErr != nil {
					rel = p
				}
				rels = append(rels, rel)
			}
			return strings.Join(rels, "\n"), nil
		})
}
