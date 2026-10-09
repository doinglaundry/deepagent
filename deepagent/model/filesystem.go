package model

import (
	"context"
	"time"
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

// FileInfo 文件信息
type FileInfo struct {
	Path       string    `json:"path"`
	IsDir      bool      `json:"is_dir"`
	IsSymlink  bool      `json:"is_symlink,omitempty"`
	Size       int64     `json:"size,omitempty"`
	ModifiedAt time.Time `json:"modified_at,omitempty"`
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

// 常量定义
const (
	// DefaultReadLimit 默认读取行数限制
	DefaultReadLimit = 2000

	// MaxFileSizeMB 最大文件大小（MB）
	MaxFileSizeMB = 10

	// ToolResultTokenLimit 工具结果的 token 限制
	ToolResultTokenLimit = 20000
)

type Filesystem interface {
	GetRoot() string
	Resolve(context.Context, string, bool) (string, error)
	List(context.Context, string) ([]FileInfo, error)
	Read(context.Context, string, *int, *int) (string, error)
	Write(context.Context, string, string) (*WriteResult, error)
	Edit(context.Context, string, string, string, bool) (*EditResult, error)
	Delete(context.Context, string) (string, error)
	Glob(context.Context, string, string) ([]FileInfo, error)
	Grep(context.Context, string, string, string) ([]GrepMatch, error)
	ApplyPatch(context.Context, string) (string, error)
}

// CommandRequest describes a one-shot shell command execution.
type CommandRequest struct {
	Command        string
	WorkDir        string
	Env            map[string]string
	Timeout        time.Duration
	MaxOutputBytes int
	// KeepOutputPrefix preserves the first MaxOutputBytes instead of a rolling tail.
	KeepOutputPrefix bool
}

// CommandResult is the normalized result of a one-shot shell command.
type CommandResult struct {
	Output         string
	ExitCode       int
	TimedOut       bool
	Truncated      bool
	ShellSessionID string
}

type CommandService interface {
	Execute(context.Context, CommandRequest) (*CommandResult, error)
	Start(context.Context, CommandRequest) (string, error)
	Wait(context.Context, string, string, int) (*CommandSnapshot, error)
	Cancel(context.Context, string) error
	Close(context.Context) error
}

type CommandSnapshot struct {
	ID        string
	ThreadID  string
	Output    string
	ExitCode  int
	Done      bool
	Offset    int
	Truncated bool
	TimedOut  bool
}

// ToolFilesystem is the complete capability set exposed to one Agent thread.
// Both local and Docker filesystems implement it; tools use the same interface.
type ToolFilesystem interface {
	Filesystem
	CommandService
}

func (fileOperationError FileOperationError) Error() string {
	return string(fileOperationError)
}

// Name 返回文件名（不包含路径）
func (fileInfo *FileInfo) GetName() string {
	// 如果 Path 是空字符串，返回空
	if fileInfo.Path == "" {
		return ""
	}
	// 路径可能使用 / 或 \ 作为分隔符
	// 从路径中提取最后一部分
	for i := len(fileInfo.Path) - 1; i >= 0; i-- {
		if fileInfo.Path[i] == '/' || fileInfo.Path[i] == '\\' {
			return fileInfo.Path[i+1:]
		}
	}
	return fileInfo.Path
}
