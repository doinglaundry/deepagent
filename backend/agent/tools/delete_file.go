package tools

import (
	"context"
	"fmt"
	"os"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const deleteFileToolDesc = `Delete a file at a workspace path. Directories are refused. Missing files are reported as a normal tool result.`

type deleteFileArgs struct {
	FilePath string `json:"file_path" jsonschema:"required,description=Absolute or workspace-relative file path to delete"`
}

// GetDeleteFileTool returns the delete_file tool.
func GetDeleteFileTool() (tool.BaseTool, error) {
	return utils.InferTool("delete_file", deleteFileToolDesc,
		func(ctx context.Context, in deleteFileArgs) (string, error) {
			if msg, denied := denyOnPlanMode(ctx); denied {
				return msg, nil
			}
			// Sandbox has no delete primitive; falls through to host fs.
			path, err := getResolvedPath(in.FilePath)
			if err != nil {
				return "", err
			}
			info, err := os.Lstat(path)
			if err != nil {
				if os.IsNotExist(err) {
					return "File does not exist: " + path, nil
				}
				return "", err
			}
			if info.IsDir() {
				return "", fmt.Errorf("refusing to delete directory: %s", path)
			}
			if err := os.Remove(path); err != nil {
				return "", err
			}
			return "Deleted file " + path, nil
		})
}
