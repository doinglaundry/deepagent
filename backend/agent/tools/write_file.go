package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type writeFileArgs struct {
	FilePath string `json:"file_path" jsonschema:"description=The path to the file to write"`
	Content  string `json:"content"   jsonschema:"description=The content to write to the file"`
}

// GetWriteFileTool returns the "write_file" tool. Creates parent dirs as
// needed; reports the path the model passed in (not the absolute resolved
// one) so log lines stay relative to the agent's mental model.
func GetWriteFileTool(root string) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameWriteFile, filesystem.WriteFileToolDesc,
		func(ctx context.Context, in writeFileArgs) (string, error) {
			p := resolvePath(root, in.FilePath)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return "", err
			}
			if err := os.WriteFile(p, []byte(in.Content), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Updated file %s", in.FilePath), nil
		})
}
