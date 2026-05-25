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

type readFileArgs struct {
	FilePath string `json:"file_path" jsonschema:"description=The path to the file to read"`
	Offset   int    `json:"offset"    jsonschema:"description=The line number to start reading from. Only provide if the file is too large to read at once"`
	Limit    int    `json:"limit"     jsonschema:"description=The number of lines to read. Only provide if the file is too large to read at once."`
}

func GetReadFileTool(sandboxManager sandbox.SandboxManager) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameReadFile, filesystem.ReadFileToolDesc,
		func(ctx context.Context, in readFileArgs) (string, error) {
			if in.Offset <= 0 {
				in.Offset = 1
			}
			if in.Limit <= 0 {
				in.Limit = 2000
			}

			if hasSandboxManager(sandboxManager) {
				virtualPath, err := resolveToolPath(in.FilePath, true)
				if err != nil {
					return "", err
				}
				sb, err := getRequiredSandbox(ctx, sandboxManager)
				if err != nil {
					return "", err
				}
				content, err := sb.ReadFile(ctx, virtualPath)
				if err != nil {
					return "", err
				}
				return paginateLines(content, in.Offset, in.Limit), nil
			}

			path, err := getResolvedPath(in.FilePath)
			if err != nil {
				return "", fmt.Errorf("路径解析失败: %w", err)
			}

			info, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Sprintf("File not found: %s", path), nil
				}
				return "", fmt.Errorf("获取文件信息失败: %w", err)
			}
			if info.IsDir() {
				return "", fmt.Errorf("path is a directory: %s", path)
			}

			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Sprintf("File not found: %s", path), nil
				}
				return "", fmt.Errorf("读取文件失败: %w", err)
			}

			return paginateLines(string(data), in.Offset, in.Limit), nil
		})
}

// paginateLines is cat -n style pagination shared by sandbox + host-fs paths.
func paginateLines(content string, offset, limit int) string {
	lines := strings.Split(content, "\n")
	start := min(offset-1, len(lines))
	end := min(start+limit, len(lines))
	out := make([]string, end-start)
	for i, line := range lines[start:end] {
		out[i] = fmt.Sprintf("%6d\t%s", offset+i, line)
	}
	return strings.Join(out, "\n")
}
