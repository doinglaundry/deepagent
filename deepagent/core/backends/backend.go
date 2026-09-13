// Package backends 提供可插拔的文件存储和命令执行后端
package backends

import (
	"context"
	"time"

	serialiser "eino-cli/deepagent/serialiser"
)

// FileOperationError 文件操作错误类型
type FileOperationError string

const (
	ErrFileNotFound     FileOperationError = "file_not_found"
	ErrPermissionDenied FileOperationError = "permission_denied"
	ErrIsDirectory      FileOperationError = "is_directory"
	ErrInvalidPath      FileOperationError = "invalid_path"
	ErrAlreadyExists    FileOperationError = "already_exists"
	ErrSandboxFsFailed  FileOperationError = "sandbox_fs_failed"
)

func (e FileOperationError) Error() string {
	return string(e)
}

type CommonToolResult struct {
	Data   any    `json:"data"`
	Errmsg string `json:"errmsg"`
}

func (c CommonToolResult) String() string {
	return serialiser.ToString(c)
}

// FileInfo 文件信息
type FileInfo struct {
	Path       string    `json:"path"`
	IsDir      bool      `json:"is_dir"`
	IsSymlink  bool      `json:"is_symlink,omitempty"`
	Size       int64     `json:"size,omitempty"`
	ModifiedAt time.Time `json:"modified_at,omitempty"`
}

// Name 返回文件名（不包含路径）
func (f *FileInfo) Name() string {
	// 如果 Path 是空字符串，返回空
	if f.Path == "" {
		return ""
	}
	// 路径可能使用 / 或 \ 作为分隔符
	// 从路径中提取最后一部分
	for i := len(f.Path) - 1; i >= 0; i-- {
		if f.Path[i] == '/' || f.Path[i] == '\\' {
			return f.Path[i+1:]
		}
	}
	return f.Path
}

// FileData 文件数据（用于状态存储）
type FileData struct {
	Content    []string  `json:"content"`     // 文件内容按行存储
	CreatedAt  time.Time `json:"created_at"`  // 创建时间
	ModifiedAt time.Time `json:"modified_at"` // 修改时间
}

// GrepMatch grep 搜索匹配结果
type GrepMatch struct {
	Path string `json:"path"` // 文件路径
	Line int    `json:"line"` // 行号
	Text string `json:"text"` // 匹配的行内容
}

// WriteResult 写入操作结果
type WriteResult struct {
	Path        string               `json:"path"`
	Error       FileOperationError   `json:"error,omitempty"`
	FilesUpdate map[string]*FileData `json:"files_update,omitempty"`
}

// EditResult 编辑操作结果
type EditResult struct {
	Path        string               `json:"path"`
	Error       FileOperationError   `json:"error,omitempty"`
	Occurrences int                  `json:"occurrences"` // 替换次数
	FilesUpdate map[string]*FileData `json:"files_update,omitempty"`
}

// ExecuteResponse 命令执行响应
type ExecuteResponse struct {
	Output         string `json:"output"`
	ExitCode       int    `json:"exit_code"`
	Truncated      bool   `json:"truncated,omitempty"` // 输出是否被截断
	ShellSessionID string
}

// CommandRequest describes a one-shot shell command execution.
type CommandRequest struct {
	Command        string
	WorkDir        string
	Env            map[string]string
	Timeout        time.Duration
	MaxOutputBytes int
}

// CommandResult is the normalized result of a one-shot shell command.
type CommandResult struct {
	Output         string
	ExitCode       int
	TimedOut       bool
	Truncated      bool
	ShellSessionID string
}

// FileUploadResponse 文件上传响应
type FileUploadResponse struct {
	Path  string             `json:"path"`
	Error FileOperationError `json:"error,omitempty"`
}

// FileDownloadResponse 文件下载响应
type FileDownloadResponse struct {
	Path    string             `json:"path"`
	Content []byte             `json:"content,omitempty"`
	Error   FileOperationError `json:"error,omitempty"`
}

// Backend 后端接口
// 定义文件操作的统一接口
type Backend interface {
	// LsInfo 列出目录内容
	LsInfo(ctx context.Context, path string) ([]FileInfo, error)

	// Read 读取文件内容
	// offset: 起始行号（从0开始），nil 表示从头开始
	// limit: 读取的行数，nil 表示读取全部
	Read(ctx context.Context, path string, offset, limit *int) (string, error)

	// Write 写入文件
	Write(ctx context.Context, path string, content string) (*WriteResult, error)

	// Edit 编辑文件（字符串替换）
	Edit(ctx context.Context, path string, oldString, newString string, replaceAll bool) (*EditResult, error)

	// GrepRaw 搜索文件内容
	// pattern: 正则表达式
	// path: 搜索路径（文件或目录）
	// glob: 文件名过滤模式
	GrepRaw(ctx context.Context, pattern string, path string, glob string) ([]GrepMatch, error)

	// GlobInfo 使用 glob 模式匹配文件
	GlobInfo(ctx context.Context, pattern string, path string) ([]FileInfo, error)

	// UploadFiles 批量上传文件
	UploadFiles(ctx context.Context, files []struct {
		Path    string
		Content []byte
	}) ([]FileUploadResponse, error)

	// ChangeDir 切换当前工作目录
	ChangeDir(ctx context.Context, path string) error
}

// ApplyPatchBackend 支持 apply_patch 工具的后端接口
type ApplyPatchBackend interface {
	Backend

	// SupportsApplyPatch 返回当前后端是否支持 apply_patch 工具
	SupportsApplyPatch() bool

	// ApplyPatch 应用 patch 内容
	ApplyPatch(ctx context.Context, patch string) (string, error)
}

type CommandExecutor interface {
	// Execute 执行 shell 命令
	Execute(ctx context.Context, command string) (*ExecuteResponse, error)

	// ExecuteCommand 执行结构化的一次性 shell 命令。
	ExecuteCommand(ctx context.Context, req CommandRequest) (*CommandResult, error)
}

// SandboxBackend 沙箱后端接口
// 扩展 Backend 接口，支持命令执行
type SandboxBackend interface {
	Backend

	CommandExecutor

	// ID 返回后端唯一标识符
	ID() string
}

// BackendFactory 后端工厂函数类型
// 用于延迟初始化后端
type BackendFactory func(ctx context.Context) Backend

// SandboxBackendFactory 沙箱后端工厂函数类型
type SandboxBackendFactory func(ctx context.Context) SandboxBackend

// 常量定义
const (
	// DefaultReadLimit 默认读取行数限制
	DefaultReadLimit = 2000

	// MaxFileSizeMB 最大文件大小（MB）
	MaxFileSizeMB = 10

	// ToolResultTokenLimit 工具结果的 token 限制
	ToolResultTokenLimit = 20000
)
