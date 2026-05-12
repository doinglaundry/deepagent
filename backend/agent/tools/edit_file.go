package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

type editFileArgs struct {
	FilePath   string `json:"file_path"   jsonschema:"description=The path to the file to modify"`
	OldString  string `json:"old_string"  jsonschema:"description=The text to replace"`
	NewString  string `json:"new_string"  jsonschema:"description=The text to replace it with (must be different from old_string)"`
	ReplaceAll bool   `json:"replace_all" jsonschema:"description=Replace all occurrences of old_string (default false),default=false"`
}

// GetEditFileTool returns the "edit_file" tool. ReplaceAll=false (the
// default) requires old_string to appear exactly once — this guards against
// the model accidentally rewriting many occurrences when it only meant one.
// ReplaceAll=true skips the uniqueness check and rewrites every match.
func GetEditFileTool(root string) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameEditFile, filesystem.EditFileToolDesc,
		func(ctx context.Context, in editFileArgs) (string, error) {
			if in.OldString == "" {
				return "", fmt.Errorf("old_string must not be empty")
			}
			p := resolvePath(root, in.FilePath)
			data, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			content := string(data)

			var updated string
			if in.ReplaceAll {
				updated = strings.ReplaceAll(content, in.OldString, in.NewString)
			} else {
				count := strings.Count(content, in.OldString)
				if count == 0 {
					return "", fmt.Errorf("old_string not found in file")
				}
				if count > 1 {
					return "", fmt.Errorf("old_string appears %d times; set replace_all=true or make it unique", count)
				}
				updated = strings.Replace(content, in.OldString, in.NewString, 1)
			}
			if err := os.WriteFile(p, []byte(updated), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Successfully replaced the string in '%s'", in.FilePath), nil
		})
}
