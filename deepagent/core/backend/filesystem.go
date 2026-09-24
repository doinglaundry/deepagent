package backend

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// FilesystemBackend 真实文件系统后端
type LocalFilesystem struct {
	rootDir       string // 根目录
	virtualMode   bool   // 虚拟模式（限制在根目录下）
	maxFileSizeMB int    // 最大文件大小（MB）
	commands      *Commands
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

type FilesystemBackend = LocalFilesystem

// NewFilesystemBackend is the compatibility constructor for LocalFilesystem.
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

	b := &LocalFilesystem{
		rootDir:       rootDir,
		virtualMode:   cfg.VirtualMode,
		maxFileSizeMB: maxFileSize,
	}
	b.commands = NewCommands("standalone", b)
	return b
}

// resolvePath 解析和验证路径
func (b *LocalFilesystem) resolvePath(path string) (string, error) {
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

func (b *LocalFilesystem) RootDir() string { return b.rootDir }

// LsInfo 列出目录内容

// Read 读取文件内容

// Write 写入文件

// Edit 编辑文件

// GrepRaw 搜索文件内容

func (b *LocalFilesystem) ChangeDir(ctx context.Context, path string) error {
	absPath, err := b.resolvePath(path)
	if err != nil {
		return err
	}

	return os.Chdir(absPath)
}

func (b *LocalFilesystem) SupportsApplyPatch() bool {
	return true
}

func (b *LocalFilesystem) ApplyPatch(ctx context.Context, patch string) (string, error) {
	return ApplyWorkspacePatch(ctx, b, patch)
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

func envPairs(env map[string]string) []string {
	pairs := make([]string, 0, len(env))
	for k, v := range env {
		pairs = append(pairs, k+"="+v)
	}
	return pairs
}

var _ Filesystem = (*LocalFilesystem)(nil)
