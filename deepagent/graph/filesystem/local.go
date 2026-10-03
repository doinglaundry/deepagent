package filesystem

import (
	"bufio"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type LocalFilesystem struct {
	rootDir       string // 根目录
	virtualMode   bool   // 虚拟模式（限制在根目录下）
	maxFileSizeMB int    // 最大文件大小（MB）
	commands      *Commands
}

// LocalFilesystemConfig configures a local filesystem.
type LocalFilesystemConfig struct {
	// RootDir 根目录，所有操作相对于此目录
	RootDir string

	// VirtualMode 虚拟模式
	// 启用后，所有路径都被限制在 RootDir 下
	VirtualMode bool

	// MaxFileSizeMB 最大文件大小（MB）
	// 默认 10MB
	MaxFileSizeMB int
}

func NewLocalFilesystem(cfg *LocalFilesystemConfig, threadID string) (*LocalFilesystem, error) {
	if cfg == nil || threadID == "" {
		return nil, fmt.Errorf("local filesystem config and thread ID are required")
	}
	rootDir := cfg.RootDir
	if rootDir == "" {
		var err error
		rootDir, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	rootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}
	maxFileSize := cfg.MaxFileSizeMB
	if maxFileSize <= 0 {
		maxFileSize = MaxFileSizeMB
	}
	b := &LocalFilesystem{rootDir: rootDir, virtualMode: cfg.VirtualMode, maxFileSizeMB: maxFileSize}
	info, err := os.Stat(b.Root())
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory")
	}
	b.commands = NewCommands(threadID, b)
	return b, nil
}

func (b *LocalFilesystem) Execute(ctx context.Context, req CommandRequest) (*CommandResult, error) {
	return b.commands.Execute(ctx, req)
}
func (b *LocalFilesystem) Start(ctx context.Context, req CommandRequest) (string, error) {
	return b.commands.Start(ctx, req)
}
func (b *LocalFilesystem) Wait(ctx context.Context, id, pattern string, offset int) (*CommandSnapshot, error) {
	return b.commands.Wait(ctx, id, pattern, offset)
}
func (b *LocalFilesystem) Cancel(ctx context.Context, id string) error {
	return b.commands.Cancel(ctx, id)
}
func (b *LocalFilesystem) Close(ctx context.Context) error { return b.commands.Close(ctx) }

var _ Filesystem = (*LocalFilesystem)(nil)
var _ CommandService = (*LocalFilesystem)(nil)
var _ ToolFilesystem = (*LocalFilesystem)(nil)
var _ patchFilesystem = (*LocalFilesystem)(nil)

func (b *LocalFilesystem) resolvePath(path string) (string, error) {
	// 安全检查：禁止路径遍历
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
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

func (b *LocalFilesystem) ApplyPatch(ctx context.Context, patch string) (string, error) {
	return ApplyWorkspacePatch(ctx, b, patch)
}

// globMaxResults glob 工具最大返回结果数
const globMaxResults = 1000

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

func (b *LocalFilesystem) Root() string { return b.rootDir }

// Every file operation uses os.Root, so an intermediate symlink replacement
// cannot turn a previously validated path into a host filesystem access.
func (b *LocalFilesystem) openRoot(ctx context.Context, path string) (*os.Root, string, error) {
	err := ctx.Err()
	if err != nil {
		return nil, "", err
	}
	absolute, err := b.resolvePath(path)
	if err != nil {
		return nil, "", err
	}
	relative, err := filepath.Rel(b.rootDir, absolute)
	if err != nil || !filepath.IsLocal(relative) {
		return nil, "", ErrInvalidPath
	}
	root, err := os.OpenRoot(b.rootDir)
	if err != nil {
		return nil, "", err
	}
	return root, relative, nil
}
func (b *LocalFilesystem) Resolve(ctx context.Context, path string, write bool) (string, error) {
	root, relative, err := b.openRoot(ctx, path)
	if err != nil {
		return "", err
	}
	defer root.Close()
	check := relative
	for {
		_, err = root.Stat(check)
		if err == nil {
			break
		}
		if !write || !os.IsNotExist(err) || check == "." {
			return "", err
		}
		check = filepath.Dir(check)
	}
	return filepath.Join(b.rootDir, relative), nil
}
func (b *LocalFilesystem) FileExists(ctx context.Context, path string) (bool, error) {
	root, relative, err := b.openRoot(ctx, path)
	if err != nil {
		return false, err
	}
	defer root.Close()
	_, err = root.Lstat(relative)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
func (b *LocalFilesystem) readBytes(ctx context.Context, path string) ([]byte, error) {
	root, relative, err := b.openRoot(ctx, path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	file, err := root.Open(relative)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", path)
	}
	limit := int64(b.maxFileSizeMB) << 20
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("file exceeds %d MiB", b.maxFileSizeMB)
	}
	err = ctx.Err()
	if err != nil {
		return nil, err
	}
	return data, nil
}
func (b *LocalFilesystem) Read(ctx context.Context, path string, offset, limit *int) (string, error) {
	data, err := b.readBytes(ctx, path)
	if err != nil {
		return "", err
	}
	return ReadFileLines(string(data), offset, limit), nil
}
func (b *LocalFilesystem) Write(ctx context.Context, path, content string) (*WriteResult, error) {
	root, relative, err := b.openRoot(ctx, path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	err = root.MkdirAll(filepath.Dir(relative), 0755)
	if err != nil {
		return nil, err
	}
	err = root.WriteFile(relative, []byte(content), 0644)
	if err != nil {
		return nil, err
	}
	return &WriteResult{Path: path}, nil
}
func (b *LocalFilesystem) CreateFileNoReplace(ctx context.Context, path, content string) (*WriteResult, error) {
	root, relative, err := b.openRoot(ctx, path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	err = root.MkdirAll(filepath.Dir(relative), 0755)
	if err != nil {
		return nil, err
	}
	parent, err := root.OpenRoot(filepath.Dir(relative))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	root = parent
	relative = filepath.Base(relative)
	temporary := ".patch-" + rand.Text()
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return nil, err
	}
	defer root.Remove(temporary)
	_, writeErr := file.WriteString(content)
	closeErr := file.Close()
	err = errors.Join(writeErr, closeErr, ctx.Err())
	if err != nil {
		return nil, err
	}
	err = root.Link(temporary, relative)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, path)
		}
		return nil, err
	}
	return &WriteResult{Path: path}, nil
}

func (b *LocalFilesystem) Edit(ctx context.Context, path, old, new string, all bool) (*EditResult, error) {
	if old == "" {
		return nil, fmt.Errorf("old text is required")
	}
	data, err := b.readBytes(ctx, path)
	if err != nil {
		return nil, err
	}
	updated, count, err := ReplaceFileText(string(data), old, new, all)
	if err != nil {
		return &EditResult{Path: path, Occurrences: count}, err
	}
	_, err = b.Write(ctx, path, updated)
	if err != nil {
		return nil, err
	}
	return &EditResult{Path: path, Occurrences: count}, nil
}
func (b *LocalFilesystem) Delete(ctx context.Context, path string) (string, error) {
	root, relative, err := b.openRoot(ctx, path)
	if err != nil {
		return "", err
	}
	defer root.Close()
	info, err := root.Lstat(relative)
	if os.IsNotExist(err) {
		return "File does not exist: " + path, nil
	}
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("refusing to delete directory: %s", path)
	}
	err = root.Remove(relative)
	if err != nil {
		return "", err
	}
	return "Deleted file " + path, nil
}
func (b *LocalFilesystem) List(ctx context.Context, path string) ([]FileInfo, error) {
	root, relative, err := b.openRoot(ctx, path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	directory, err := root.Open(relative)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		contextErr := ctx.Err()
		if contextErr != nil {
			return nil, contextErr
		}
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, FileInfo{Path: filepath.Join(path, entry.Name()), IsDir: entry.IsDir(), IsSymlink: entry.Type()&os.ModeSymlink != 0, Size: info.Size(), ModifiedAt: info.ModTime()})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return out[i].Path < out[j].Path
	})
	return out, nil
}
func (b *LocalFilesystem) Glob(ctx context.Context, pattern, path string) ([]FileInfo, error) {
	root, relative, err := b.openRoot(ctx, path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var out []FileInfo
	err = fs.WalkDir(root.FS(), filepath.ToSlash(relative), func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		contextErr := ctx.Err()
		if contextErr != nil {
			return contextErr
		}
		if name != relative && entry.IsDir() && shouldSkipDir(entry.Name()) {
			return fs.SkipDir
		}
		local, err := filepath.Rel(relative, name)
		if err != nil {
			return err
		}
		if local == "." || !globMatch(pattern, local) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		out = append(out, FileInfo{Path: name, IsDir: entry.IsDir(), IsSymlink: entry.Type()&os.ModeSymlink != 0, Size: info.Size(), ModifiedAt: info.ModTime()})
		if len(out) >= globMaxResults {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
func (b *LocalFilesystem) Grep(ctx context.Context, pattern, path, glob string) ([]GrepMatch, error) {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	root, relative, err := b.openRoot(ctx, path)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	var out []GrepMatch
	err = fs.WalkDir(root.FS(), filepath.ToSlash(relative), func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		contextErr := ctx.Err()
		if contextErr != nil {
			return contextErr
		}
		if entry.IsDir() {
			if name != relative && shouldSkipDir(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if glob != "" && !globMatch(glob, entry.Name()) && !globMatch(glob, name) {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > int64(b.maxFileSizeMB)<<20 {
			return nil
		}
		file, err := root.Open(name)
		if err != nil {
			return err
		}
		defer file.Close()
		scanner := bufio.NewScanner(io.LimitReader(file, int64(b.maxFileSizeMB)<<20))
		scanner.Buffer(make([]byte, 4096), 1<<20)
		line := 0
		for scanner.Scan() {
			contextErr := ctx.Err()
			if contextErr != nil {
				return contextErr
			}
			line++
			text := scanner.Text()
			if strings.ContainsRune(text, 0) {
				return nil
			}
			if re.MatchString(text) {
				out = append(out, GrepMatch{Path: name, Line: line, Text: text})
				if len(out) >= 100 {
					return fs.SkipAll
				}
			}
		}
		return scanner.Err()
	})
	return out, err
}
func (b *LocalFilesystem) UploadFiles(ctx context.Context, files []struct {
	Path    string
	Content []byte
}) ([]FileUploadResponse, error) {
	out := make([]FileUploadResponse, 0, len(files))
	for _, file := range files {
		contextErr := ctx.Err()
		if contextErr != nil {
			return nil, contextErr
		}
		result, err := b.Write(ctx, file.Path, string(file.Content))
		response := FileUploadResponse{Path: file.Path}
		if err != nil {
			response.Error = ErrInvalidPath
		} else if result != nil {
			response.Error = result.Error
		}
		out = append(out, response)
	}
	return out, nil
}
func (b *LocalFilesystem) DownloadFiles(ctx context.Context, paths []string) ([]FileDownloadResponse, error) {
	out := make([]FileDownloadResponse, 0, len(paths))
	for _, path := range paths {
		contextErr := ctx.Err()
		if contextErr != nil {
			return nil, contextErr
		}
		data, err := b.readBytes(ctx, path)
		response := FileDownloadResponse{Path: path, Content: data}
		if err != nil {
			response.Error = ErrInvalidPath
		}
		out = append(out, response)
	}
	return out, nil
}
