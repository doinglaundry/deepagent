package backends

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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
	if strings.Contains(path, "..") {
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

// LsInfo 列出目录内容
func (b *FilesystemBackend) LsInfo(ctx context.Context, path string) ([]FileInfo, error) {
	absPath, err := b.resolvePath(path)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		if os.IsPermission(err) {
			return nil, ErrPermissionDenied
		}
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return nil, ErrInvalidPath
	}

	entries, err := os.ReadDir(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrFileNotFound
		}
		if os.IsPermission(err) {
			return nil, ErrPermissionDenied
		}
		return nil, err
	}

	var results []FileInfo
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		modTime := info.ModTime()
		fi := FileInfo{
			Path:       filepath.Join(path, entry.Name()),
			IsDir:      entry.IsDir(),
			IsSymlink:  entry.Type()&fs.ModeSymlink != 0,
			Size:       info.Size(),
			ModifiedAt: modTime,
		}
		results = append(results, fi)
	}

	// 排序：目录优先，然后按名称
	sort.Slice(results, func(i, j int) bool {
		if results[i].IsDir != results[j].IsDir {
			return results[i].IsDir
		}
		return results[i].Path < results[j].Path
	})

	return results, nil
}

// Read 读取文件内容
func (b *FilesystemBackend) Read(ctx context.Context, path string, offset, limit *int) (content string, err error) {
	absPath, err := b.resolvePath(path)
	if err != nil {
		return "", err
	}

	// 检查是否是目录
	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", ErrFileNotFound
		}
		return "", err
	}
	if info.IsDir() {
		return "", ErrIsDirectory
	}

	// 检查文件大小
	if info.Size() > int64(b.maxFileSizeMB)*1024*1024 {
		return "", fmt.Errorf("文件过大（超过 %dMB 限制）", b.maxFileSizeMB)
	}

	contentBytes, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsPermission(err) {
			return "", ErrPermissionDenied
		}
		return "", err
	}

	return ReadFileLines(string(contentBytes), offset, limit), nil
}

// Write 写入文件
func (b *FilesystemBackend) Write(ctx context.Context, path string, content string) (*WriteResult, error) {
	absPath, err := b.resolvePath(path)
	if err != nil {
		return &WriteResult{Path: path, Error: ErrInvalidPath}, nil
	}

	// 确保目录存在
	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return &WriteResult{Path: path, Error: ErrPermissionDenied}, nil
	}

	// 写入文件
	if err := os.WriteFile(absPath, []byte(content), 0644); err != nil {
		if os.IsPermission(err) {
			return &WriteResult{Path: path, Error: ErrPermissionDenied}, nil
		}
		return nil, err
	}

	return &WriteResult{Path: path}, nil
}

// Edit 编辑文件
func (b *FilesystemBackend) Edit(ctx context.Context, path string, oldString, newString string, replaceAll bool) (result *EditResult, err error) {
	absPath, err := b.resolvePath(path)
	if err != nil {
		return &EditResult{Path: path, Error: ErrInvalidPath}, nil
	}

	// 读取文件
	content, err := os.ReadFile(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &EditResult{Path: path, Error: ErrFileNotFound}, nil
		}
		if os.IsPermission(err) {
			return &EditResult{Path: path, Error: ErrPermissionDenied}, nil
		}
		return nil, err
	}

	newContent, occurrences, err := ReplaceFileText(string(content), oldString, newString, replaceAll)
	if err != nil {
		return &EditResult{Path: path, Occurrences: occurrences}, err
	}
	// 写回文件
	if err := os.WriteFile(absPath, []byte(newContent), 0644); err != nil {
		if os.IsPermission(err) {
			return &EditResult{Path: path, Error: ErrPermissionDenied}, nil
		}
		return nil, err
	}

	return &EditResult{
		Path:        path,
		Occurrences: occurrences,
	}, nil
}

// GrepRaw 搜索文件内容
func (b *FilesystemBackend) GrepRaw(ctx context.Context, pattern string, searchPath string, glob string) ([]GrepMatch, error) {
	absPath, err := b.resolvePath(searchPath)
	if err != nil {
		return nil, err
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("无效的正则表达式: %w", err)
	}

	var matches []GrepMatch

	err = filepath.WalkDir(absPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}

		// glob 过滤
		if glob != "" {
			matched, _ := filepath.Match(glob, d.Name())
			if !matched {
				return nil
			}
		}

		// 检查文件大小
		info, err := d.Info()
		if err != nil || info.Size() > int64(b.maxFileSizeMB)*1024*1024 {
			return nil
		}

		// 读取和搜索文件
		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()

		relPath, _ := filepath.Rel(b.rootDir, path)
		if relPath == "" {
			relPath = path
		}

		scanner := bufio.NewScanner(file)
		lineNum := 0

		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if re.MatchString(line) {
				matches = append(matches, GrepMatch{
					Path: relPath,
					Line: lineNum,
					Text: line,
				})

				// 限制结果数量
				if len(matches) >= 100 {
					return filepath.SkipAll
				}
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return matches, nil
}

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
func (b *FilesystemBackend) GlobInfo(ctx context.Context, pattern string, searchPath string) ([]FileInfo, error) {
	absPath, err := b.resolvePath(searchPath)
	if err != nil {
		return nil, err
	}

	var results []FileInfo

	err = filepath.WalkDir(absPath, func(path string, d fs.DirEntry, err error) error {
		// 支持 context 取消，避免遍历大目录时无法中断
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err != nil {
			return nil
		}

		// 跳过隐藏目录（如 .git, node_modules 等大目录）
		if d.IsDir() && shouldSkipDir(d.Name()) {
			return filepath.SkipDir
		}

		relPath, _ := filepath.Rel(absPath, path)
		if relPath == "." {
			return nil
		}

		matched := globMatch(pattern, relPath)
		if !matched {
			return nil
		}

		// 计算相对于 rootDir 的路径用于返回
		displayPath, _ := filepath.Rel(b.rootDir, path)
		if displayPath == "" {
			displayPath = path
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		modTime := info.ModTime()
		results = append(results, FileInfo{
			Path:       displayPath,
			IsDir:      d.IsDir(),
			Size:       info.Size(),
			ModifiedAt: modTime,
		})

		if len(results) >= globMaxResults {
			return filepath.SkipAll
		}

		return nil
	})

	if err != nil && err != filepath.SkipAll && ctx.Err() == nil {
		return nil, err
	}

	// 排序
	sort.Slice(results, func(i, j int) bool {
		return results[i].Path < results[j].Path
	})

	return results, nil
}

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
func (b *FilesystemBackend) UploadFiles(ctx context.Context, files []struct {
	Path    string
	Content []byte
}) ([]FileUploadResponse, error) {
	responses := make([]FileUploadResponse, len(files))

	for i, file := range files {
		absPath, err := b.resolvePath(file.Path)
		if err != nil {
			responses[i] = FileUploadResponse{Path: file.Path, Error: ErrInvalidPath}
			continue
		}

		// 确保目录存在
		dir := filepath.Dir(absPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			responses[i] = FileUploadResponse{Path: file.Path, Error: ErrPermissionDenied}
			continue
		}

		// 写入文件
		if err := os.WriteFile(absPath, file.Content, 0644); err != nil {
			if os.IsPermission(err) {
				responses[i] = FileUploadResponse{Path: file.Path, Error: ErrPermissionDenied}
			} else {
				responses[i] = FileUploadResponse{Path: file.Path, Error: ErrInvalidPath}
			}
			continue
		}

		responses[i] = FileUploadResponse{Path: file.Path}
	}

	return responses, nil
}

// DownloadFiles 批量下载文件
func (b *FilesystemBackend) DownloadFiles(ctx context.Context, paths []string) ([]FileDownloadResponse, error) {
	responses := make([]FileDownloadResponse, len(paths))

	for i, path := range paths {
		absPath, err := b.resolvePath(path)
		if err != nil {
			responses[i] = FileDownloadResponse{Path: path, Error: ErrInvalidPath}
			continue
		}

		info, err := os.Lstat(absPath)
		if err != nil {
			if os.IsNotExist(err) {
				responses[i] = FileDownloadResponse{Path: path, Error: ErrFileNotFound}
			} else {
				responses[i] = FileDownloadResponse{Path: path, Error: ErrPermissionDenied}
			}
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			responses[i] = FileDownloadResponse{Path: path, Error: ErrInvalidPath}
			continue
		}
		if info.IsDir() {
			responses[i] = FileDownloadResponse{Path: path, Error: ErrIsDirectory}
			continue
		}

		// 读取文件
		content, err := os.ReadFile(absPath)
		if err != nil {
			if os.IsPermission(err) {
				responses[i] = FileDownloadResponse{Path: path, Error: ErrPermissionDenied}
			} else {
				responses[i] = FileDownloadResponse{Path: path, Error: ErrFileNotFound}
			}
			continue
		}

		responses[i] = FileDownloadResponse{Path: path, Content: content}
	}

	return responses, nil
}

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
func (b *SandboxFilesystemBackend) ExecuteCommand(ctx context.Context, req CommandRequest) (result *CommandResult, err error) {
	execCtx := ctx
	cancel := func() {}
	if req.Timeout > 0 {
		execCtx, cancel = context.WithTimeout(ctx, req.Timeout)
	}
	defer cancel()

	workDir, err := b.commandWorkDir(req.WorkDir)
	if err != nil {
		return nil, err
	}

	// 尝试多个可能的 shell 路径，优先使用绝对路径
	shells := []string{"/bin/bash", "/bin/sh", "/usr/bin/bash", "/usr/bin/sh", "bash", "sh"}
	var lastErr error

	for _, shell := range shells {
		cmd := exec.CommandContext(execCtx, shell, "-c", req.Command)
		configureShellCommandCancel(cmd)
		cmd.Dir = workDir
		if len(req.Env) > 0 {
			cmd.Env = append(os.Environ(), envPairs(req.Env)...)
		}

		output, err := cmd.CombinedOutput()

		// 如果不是 "executable file not found" 错误，说明找到了可用的 shell
		if err == nil || !strings.Contains(err.Error(), "executable file not found") {
			response := &CommandResult{
				Output:   string(output),
				ExitCode: 0,
				TimedOut: execCtx.Err() == context.DeadlineExceeded,
			}

			if err != nil {
				if execCtx.Err() != nil {
					response.ExitCode = -1
					return response, nil
				}
				if exitErr, ok := err.(*exec.ExitError); ok {
					response.ExitCode = exitErr.ExitCode()
				} else {
					lastErr = err
					continue
				}
			}

			if req.MaxOutputBytes > 0 && len(response.Output) > req.MaxOutputBytes {
				response.Output = response.Output[:req.MaxOutputBytes]
				response.Truncated = true
			}

			return response, nil
		}
		lastErr = err
	}

	return nil, fmt.Errorf("no shell available: %v", lastErr)
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
var _ SandboxBackend = (*SandboxFilesystemBackend)(nil)
