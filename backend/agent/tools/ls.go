package tools

import (
	"context"
	"os"
	"strings"

	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// lsArgs mirrors eino's filesystem.lsArgs byte-for-byte: a single `path`
// field with NO description tag, so the generated schema matches.
type lsArgs struct {
	Path string `json:"path"`
}

// GetLsTool returns the "ls" tool. Lists immediate entries under path
// (resolved against root); no recursion, no hidden-file filtering — matches
// eino's filesystem.newLsTool behavior.
func GetLsTool(root string) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameLs, filesystem.ListFilesToolDesc,
		func(ctx context.Context, in lsArgs) (string, error) {
			entries, err := os.ReadDir(resolvePath(root, in.Path))
			if err != nil {
				return "", err
			}
			if len(entries) == 0 {
				return noFilesFound, nil
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			return strings.Join(names, "\n"), nil
		})
}
