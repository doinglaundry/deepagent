package backend

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const applyPatchBinEnv = "APPLY_PATCH_BIN"

// FilesystemBackend 真实文件系统后端
type FilesystemBackend struct {
	rootDir       string // 根目录
	virtualMode   bool   // 虚拟模式（限制在根目录下）
	maxFileSizeMB int    // 最大文件大小（MB）
}

// FilesystemBackendConfig 文件系统后端配置
type FilesystemBackendConfig struct {
	// RootDir 根目录，所有操作相对于此目录
	RootDir string

	// VirtualMode 虚拟模式
	// 启用后，所有路径都被限制在 RootDir 下
	VirtualMode bool

	// MaxFileSizeMB 最大文件大小（MB）
	// 默认 10MB
	MaxFileSizeMB int
}

// NewFilesystemBackend 创建文件系统后端
func NewFilesystemBackend(cfg *FilesystemBackendConfig) *FilesystemBackend {
	rootDir := cfg.RootDir
	if rootDir == "" {
		rootDir, _ = os.Getwd()
	}
	rootDir, _ = filepath.Abs(rootDir)

	maxFileSize := cfg.MaxFileSizeMB
	if maxFileSize <= 0 {
		maxFileSize = MaxFileSizeMB
	}

	return &FilesystemBackend{
		rootDir:       rootDir,
		virtualMode:   cfg.VirtualMode,
		maxFileSizeMB: maxFileSize,
	}
}

// resolvePath 解析和验证路径
func (b *FilesystemBackend) resolvePath(path string) (string, error) {
	// 安全检查：禁止路径遍历
	if clean := filepath.Clean(path); clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", ErrInvalidPath
	}

	// 处理相对路径
	var absPath string
	if filepath.IsAbs(path) {
		if b.virtualMode {
			cleaned := filepath.Clean(path)
			if pathWithinRoot(cleaned, b.rootDir) {
				// Models may echo the absolute workspace path supplied in their
				// runtime context. Keep an already sandboxed path unchanged.
				absPath = cleaned
			} else {
				// Other absolute paths retain the virtual-root behavior: /etc/x
				// addresses <rootDir>/etc/x rather than the host filesystem.
				absPath = filepath.Join(b.rootDir, strings.TrimLeft(cleaned, string(filepath.Separator)))
			}
		} else {
			absPath = path
		}
	} else {
		absPath = filepath.Join(b.rootDir, path)
	}

	absPath = filepath.Clean(absPath)

	// 虚拟模式下，确保路径在 rootDir 下
	if b.virtualMode {
		if !pathWithinRoot(absPath, b.rootDir) {
			return "", ErrInvalidPath
		}
	}

	return absPath, nil
}

func pathWithinRoot(path, root string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

func (b *FilesystemBackend) RootDir() string { return b.rootDir }

// LsInfo 列出目录内容

// Read 读取文件内容

// Write 写入文件

// Edit 编辑文件

// GrepRaw 搜索文件内容

func (b *FilesystemBackend) ChangeDir(ctx context.Context, path string) error {
	absPath, err := b.resolvePath(path)
	if err != nil {
		return err
	}

	return os.Chdir(absPath)
}

func (b *FilesystemBackend) SupportsApplyPatch() bool {
	_, ok := b.resolveApplyPatchBin()
	return ok
}

func (b *FilesystemBackend) ApplyPatch(ctx context.Context, patch string) (string, error) {
	binPath, ok := b.resolveApplyPatchBin()
	if !ok {
		return "", fmt.Errorf("%s is not set to a valid apply_patch executable", applyPatchBinEnv)
	}

	cmd := exec.CommandContext(ctx, binPath)
	cmd.Dir = b.rootDir
	cmd.Stdin = strings.NewReader(patch)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		output := formatApplyPatchOutput(stdout.String(), stderr.String())
		if output != "" {
			err = fmt.Errorf("%w\n%s", err, output)
		}
		slog.ErrorContext(ctx, fmt.Sprintf("[FilesystemBackend::ApplyPatch] fail with error:%v", err))
		return "", err
	}

	return formatApplyPatchOutput(stdout.String(), stderr.String()), nil
}

func formatApplyPatchOutput(stdout, stderr string) string {
	var sb strings.Builder

	if stdout != "" {
		sb.WriteString("stdout>\n")
		sb.WriteString(stdout)
		sb.WriteString("stdout end\n")
	}

	if stderr != "" {
		sb.WriteString("stderr>\n")
		sb.WriteString(stderr)
		sb.WriteString("stderr end\n")
	}
	return sb.String()
}

func (b *FilesystemBackend) resolveApplyPatchBin() (string, bool) {
	raw := strings.TrimSpace(os.Getenv(applyPatchBinEnv))
	if raw == "" {
		return "", false
	}

	resolved, err := expandUserPath(raw)
	if err != nil {
		return "", false
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() || !isExecutable(info.Mode()) {
		return "", false
	}
	return resolved, true
}

func expandUserPath(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}

func isExecutable(mode os.FileMode) bool {
	return mode.IsRegular() && mode&0o111 != 0
}

// globMaxResults glob 工具最大返回结果数
const globMaxResults = 1000

// GlobInfo 使用 glob 模式匹配文件
// 支持 ** 递归匹配（如 **/*.go），支持 context 取消，结果上限 globMaxResults 条

// shouldSkipDir 判断是否跳过某些大型或无关目录
func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".svn", ".hg", "__pycache__", ".tox", ".eggs", ".mypy_cache":
		return true
	}
	return false
}

// globMatch 匹配 glob 模式，支持 ** 递归匹配
// pattern 和 name 都使用 / 作为分隔符
func globMatch(pattern, name string) bool {
	// 统一使用 / 分隔符
	pattern = filepath.ToSlash(pattern)
	name = filepath.ToSlash(name)

	return doGlobMatch(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

// doGlobMatch 递归匹配 glob 模式的各段
func doGlobMatch(patternParts, nameParts []string) bool {
	for len(patternParts) > 0 && len(nameParts) > 0 {
		p := patternParts[0]

		if p == "**" {
			// ** 可以匹配零个或多个路径段
			patternParts = patternParts[1:]
			if len(patternParts) == 0 {
				return true // ** 在末尾，匹配所有剩余路径
			}
			// 尝试 ** 匹配 0 到 N 个路径段
			for i := 0; i <= len(nameParts); i++ {
				if doGlobMatch(patternParts, nameParts[i:]) {
					return true
				}
			}
			return false
		}

		// 使用 filepath.Match 匹配单个路径段
		matched, _ := filepath.Match(p, nameParts[0])
		if !matched {
			return false
		}

		patternParts = patternParts[1:]
		nameParts = nameParts[1:]
	}

	// 处理 pattern 末尾的 **
	for _, p := range patternParts {
		if p != "**" {
			return false
		}
	}

	return len(nameParts) == 0
}

// UploadFiles 批量上传文件

// DownloadFiles 批量下载文件

// SandboxFilesystemBackend 带命令执行的文件系统后端
type SandboxFilesystemBackend struct {
	*FilesystemBackend
	id string
}

// NewSandboxFilesystemBackend 创建沙箱文件系统后端
func NewSandboxFilesystemBackend(cfg *FilesystemBackendConfig) *SandboxFilesystemBackend {
	return &SandboxFilesystemBackend{
		FilesystemBackend: NewFilesystemBackend(cfg),
		id:                fmt.Sprintf("sandbox-%d", time.Now().UnixNano()),
	}
}

// Execute 执行 shell 命令
func (b *SandboxFilesystemBackend) Execute(ctx context.Context, command string) (*ExecuteResponse, error) {
	result, err := b.ExecuteCommand(ctx, CommandRequest{
		Command:        command,
		MaxOutputBytes: ToolResultTokenLimit * 4,
	})
	if err != nil {
		return nil, err
	}
	return &ExecuteResponse{
		Output:         result.Output,
		ExitCode:       result.ExitCode,
		Truncated:      result.Truncated,
		ShellSessionID: result.ShellSessionID,
	}, nil
}

// ExecuteCommand 执行结构化的一次性 shell 命令。
func (b *SandboxFilesystemBackend) ExecuteCommand(ctx context.Context, req CommandRequest) (*CommandResult, error) {
	service := NewCommands(b.id, b.FilesystemBackend)
	defer service.Close(context.Background())
	return service.Execute(ctx, req)
}

func (b *SandboxFilesystemBackend) commandWorkDir(workDir string) (string, error) {
	if workDir == "" {
		return b.rootDir, nil
	}
	if filepath.IsAbs(workDir) {
		cleaned := filepath.Clean(workDir)
		if cleaned == b.rootDir || strings.HasPrefix(cleaned, b.rootDir+string(os.PathSeparator)) {
			return cleaned, nil
		}
	}
	return b.resolvePath(workDir)
}

func envPairs(env map[string]string) []string {
	pairs := make([]string, 0, len(env))
	for k, v := range env {
		pairs = append(pairs, k+"="+v)
	}
	return pairs
}

// ID 返回后端唯一标识符
func (b *SandboxFilesystemBackend) ID() string {
	return b.id
}

// 确保实现接口
var _ Backend = (*FilesystemBackend)(nil)
var _ ApplyPatchBackend = (*FilesystemBackend)(nil)
var _ WorkspaceBackend = (*FilesystemBackend)(nil)
var _ SandboxBackend = (*SandboxFilesystemBackend)(nil)
