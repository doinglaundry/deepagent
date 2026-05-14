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

// readFileArgs mirrors eino filesystem.go:546-555 verbatim so InferTool
// produces an identical JSON schema (model is trained on these descriptions).
type readFileArgs struct {
	FilePath string `json:"file_path" jsonschema:"description=The path to the file to read"`
	Offset   int    `json:"offset"    jsonschema:"description=The line number to start reading from. Only provide if the file is too large to read at once"`
	Limit    int    `json:"limit"     jsonschema:"description=The number of lines to read. Only provide if the file is too large to read at once."`
}

// GetReadFileTool returns the "read_file" tool. Output format is cat -n
// style with %6d\t header per line, matching eino's filesystem.newReadFileTool.
// Defaults: Offset<=0 → 1, Limit<=0 → 2000.
func GetReadFileTool(root string) (tool.BaseTool, error) {
	return utils.InferTool(filesystem.ToolNameReadFile, filesystem.ReadFileToolDesc,
		func(ctx context.Context, in readFileArgs) (string, error) {
			if in.Offset <= 0 {
				in.Offset = 1
			}
			if in.Limit <= 0 {
				in.Limit = 2000
			}

			path := resolvePath(root, in.FilePath)
			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					return fmt.Sprintf("File not found: %s", path), nil
				}
				return "", err
			}
			lines := strings.Split(string(data), "\n")
			start := min(in.Offset-1, len(lines))
			end := min(start+in.Limit, len(lines))

			// strings.Join 只在元素之间插分隔符,天然没有结尾换行 —— 跟 eino 的
			// "最后一行 fmt 不加 \n" 行为吻合,不用 per-iteration if 判断。
			out := make([]string, end-start)
			for i, line := range lines[start:end] {
				out[i] = fmt.Sprintf("%6d\t%s", in.Offset+i, line)
			}
			return strings.Join(out, "\n"), nil
		})
}
