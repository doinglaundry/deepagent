package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"eino-cli/backend/sandbox"
)

type writeFileArgs struct {
	FilePath string `json:"file_path" jsonschema:"description=The path to the file to write"`
	Content  string `json:"content"   jsonschema:"description=The content to write to the file"`
}

// GetWriteFileTool returns the write_file tool routed through sandbox or host fs.
func GetWriteFileTool(sandboxManager sandbox.SandboxManager) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameWriteFile, filesystem.WriteFileToolDesc,
		func(ctx context.Context, in writeFileArgs) (string, error) {
			if msg, denied := denyOnPlanMode(ctx); denied {
				return msg, nil
			}
			if shouldUseSandbox(in.FilePath) {
				if sb := getSandbox(ctx, sandboxManager); sb != nil {
					if err := sb.WriteFile(ctx, in.FilePath, in.Content, false); err != nil {
						return "", err
					}
					return fmt.Sprintf("Updated file %s", in.FilePath), nil
				}
			}
			p := resolvePath(in.FilePath)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(p, []byte(in.Content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Updated file %s", in.FilePath), nil
		})
}
