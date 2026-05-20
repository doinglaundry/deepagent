package tools

import (
	"context"
	"os"
	"strings"

	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"eino-cli/backend/consts"
	"eino-cli/backend/sandbox"
)

// No description tag — matches eino's schema byte-for-byte.
type lsArgs struct {
	Path string `json:"path"`
}

// GetLsTool returns the ls tool.
func GetLsTool(root string, sandboxManager sandbox.SandboxManager) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameLs, filesystem.ListFilesToolDesc,
		func(ctx context.Context, in lsArgs) (string, error) {
			if shouldUseSandbox(in.Path) {
				if sb := getSandbox(ctx, sandboxManager); sb != nil {
					entries, err := sb.ListDir(ctx, in.Path, 1)
					if err == nil {
						if len(entries) == 0 {
							return consts.NoFilesFound, nil
						}
						return strings.Join(entries, "\n"), nil
					}
				}
			}
			entries, err := os.ReadDir(resolvePath(root, in.Path))
			if err != nil {
				return "", err
			}
			if len(entries) == 0 {
				return consts.NoFilesFound, nil
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			return strings.Join(names, "\n"), nil
		})
}
