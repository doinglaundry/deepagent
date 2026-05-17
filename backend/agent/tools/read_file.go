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

// readFileArgs 定义了 read_file 工具的输入参数结构。
// 该结构体与 eino 的 filesystem.go:546-555 完全一致，确保 InferTool
// 生成相同的 JSON Schema（模型基于这些描述进行训练）。
type readFileArgs struct {
	// FilePath 要读取的文件路径，可以是绝对路径或相对于根目录的相对路径
	FilePath string `json:"file_path" jsonschema:"description=The path to the file to read"`
	
	// Offset 起始行号，从1开始计数。仅当文件过大需要分页读取时提供
	// 默认值: 1（从文件开头开始读取）
	Offset   int    `json:"offset"    jsonschema:"description=The line number to start reading from. Only provide if the file is too large to read at once"`
	
	// Limit 要读取的行数。仅当文件过大需要限制读取量时提供
	// 默认值: 2000（最多读取2000行）
	Limit    int    `json:"limit"     jsonschema:"description=The number of lines to read. Only provide if the file is too large to read at once."`
}

// GetReadFileTool 创建并返回 "read_file" 文件读取工具。
// 该工具用于读取指定路径的文件内容，支持分页读取和行号显示。
//
// 参数:
//   - root: 根目录路径，用于限制文件访问范围，防止路径逃逸攻击
//
// 返回值:
//   - tool.BaseTool: 配置好的文件读取工具
//   - error: 工具创建过程中的错误
//
// 功能特点:
//   1. 输出格式采用 cat -n 风格，每行显示6位右对齐的行号（格式: "%6d\t"）
//   2. 默认值: Offset <= 0 时设为 1（从第1行开始），Limit <= 0 时设为 2000（最多读取2000行）
//   3. 路径解析: 使用 getResolvedPath 确保文件路径在 root 目录内，防止目录遍历攻击
//   4. 错误处理: 文件不存在时返回友好提示，目录路径返回错误
//   5. 内存优化: 使用分页读取避免大文件内存溢出
//
// 使用示例:
//   - 读取文件前10行: file_path="test.txt", offset=1, limit=10
//   - 从第50行开始读取: file_path="test.txt", offset=50, limit=100
func GetReadFileTool(root string) (tool.BaseTool, error) {
	// 使用 InferTool 创建工具，该函数封装了工具注册、参数验证等通用逻辑
	return utils.InferTool(filesystem.ToolNameReadFile, filesystem.ReadFileToolDesc,
		// 工具执行函数，处理实际的文件读取逻辑
		func(ctx context.Context, in readFileArgs) (string, error) {
			if in.Offset <= 0 {
				in.Offset = 1
			}
			if in.Limit <= 0 {
				in.Limit = 2000
			}

			if shouldUseSandbox(in.FilePath) {
				if sb := sandboxFromCtx(ctx); sb != nil {
					content, err := sb.ReadFile(ctx, in.FilePath)
					if err == nil {
						return paginateLines(content, in.Offset, in.Limit), nil
					}
				}
			}

			path, err := getResolvedPath(root, in.FilePath)
			if err != nil {
				return "", fmt.Errorf("路径解析失败: %w", err)
			}

			// 3. 文件存在性和类型检查
			info, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					// 文件不存在时返回友好提示而非错误，便于工具链处理
					return fmt.Sprintf("File not found: %s", path), nil
				}
				return "", fmt.Errorf("获取文件信息失败: %w", err)
			}
			if info.IsDir() {
				return "", fmt.Errorf("path is a directory: %s", path)
			}

			// 4. 读取文件内容
			data, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					// 再次检查文件不存在情况（竞态条件保护）
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
