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

func GetDeleteFileTool(root string) (tool.BaseTool, error) {
	return utils.InferTool("delete_file", deleteFileToolDesc,
		func(ctx context.Context, in deleteFileArgs) (string, error) {
			if msg, denied := denyOnPlanMode(ctx); denied {
				return msg, nil
			}
			// Sandbox doesn't expose a delete primitive directly: writing
			// empty bytes via UpdateFile would only truncate. For now,
			// delete operations on /mnt/... paths fall back to host fs
			// after the sandbox resolves the mount root for us — kept as
			// a follow-up. Today host fs is the only deleter.
			return deleteFile(root, in.FilePath)
		})
}

func deleteFile(root, filePath string) (string, error) {
	path, err := getResolvedPath(root, filePath)
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
}
