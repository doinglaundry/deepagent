package tools

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"eino-cli/backend/sandbox"
)

type editFileArgs struct {
	FilePath   string `json:"file_path"   jsonschema:"description=The path to the file to modify"`
	OldString  string `json:"old_string"  jsonschema:"description=The text to replace"`
	NewString  string `json:"new_string"  jsonschema:"description=The text to replace it with (must be different from old_string)"`
	ReplaceAll bool   `json:"replace_all" jsonschema:"description=Replace all occurrences of old_string (default false),default=false"`
}

// GetEditFileTool returns the edit_file tool; ReplaceAll=false requires a unique old_string.
func GetEditFileTool(root string, sandboxManager sandbox.SandboxManager) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameEditFile, filesystem.EditFileToolDesc,
		func(ctx context.Context, in editFileArgs) (string, error) {
			if in.OldString == "" {
				return "", fmt.Errorf("old_string must not be empty")
			}
			if msg, denied := denyOnPlanMode(ctx); denied {
				return msg, nil
			}
			if shouldUseSandbox(in.FilePath) {
				if sb := getSandbox(ctx, sandboxManager); sb != nil {
					content, err := sb.ReadFile(ctx, in.FilePath)
					if err != nil {
						return "", err
					}
					updated, err := applyEditReplacement(content, in.OldString, in.NewString, in.ReplaceAll)
					if err != nil {
						return "", err
					}
					if err := sb.WriteFile(ctx, in.FilePath, updated, false); err != nil {
						return "", err
					}
					return fmt.Sprintf("Successfully replaced the string in '%s'", in.FilePath), nil
				}
			}
			p := resolvePath(root, in.FilePath)
			data, err := os.ReadFile(p)
			if err != nil {
				return "", err
			}
			updated, err := applyEditReplacement(string(data), in.OldString, in.NewString, in.ReplaceAll)
			if err != nil {
				return "", err
			}
			if err := os.WriteFile(p, []byte(updated), 0o644); err != nil {
				return "", err
			}
			return fmt.Sprintf("Successfully replaced the string in '%s'", in.FilePath), nil
		})
}

func applyEditReplacement(content, oldStr, newStr string, replaceAll bool) (string, error) {
	if replaceAll {
		return strings.ReplaceAll(content, oldStr, newStr), nil
	}
	count := strings.Count(content, oldStr)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in file")
	}
	if count > 1 {
		return "", fmt.Errorf("old_string appears %d times; set replace_all=true or make it unique", count)
	}
	return strings.Replace(content, oldStr, newStr, 1), nil
}
