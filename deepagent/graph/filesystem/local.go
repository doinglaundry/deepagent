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

	agentmodel "eino-cli/deepagent/model"
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

func NewLocalFilesystem(filesystemConfig *LocalFilesystemConfig, threadID string) (*LocalFilesystem, error) {
	if filesystemConfig == nil || threadID == "" {
		return nil, fmt.Errorf("local filesystem config and thread ID are required")
	}
	rootDir := filesystemConfig.RootDir
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
	maxFileSize := filesystemConfig.MaxFileSizeMB
	if maxFileSize <= 0 {
		maxFileSize = agentmodel.MaxFileSizeMB
	}
	localFilesystem := &LocalFilesystem{rootDir: rootDir, virtualMode: filesystemConfig.VirtualMode, maxFileSizeMB: maxFileSize}
	rootInfo, err := os.Stat(localFilesystem.GetRoot())
	if err != nil {
		return nil, err
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory")
	}
	localFilesystem.commands = NewCommands(threadID, localFilesystem)
	return localFilesystem, nil
}

func (localFilesystem *LocalFilesystem) Execute(ctx context.Context, request agentmodel.CommandRequest) (*agentmodel.CommandResult, error) {
	return localFilesystem.commands.Execute(ctx, request)
}
func (localFilesystem *LocalFilesystem) Start(ctx context.Context, request agentmodel.CommandRequest) (string, error) {
	return localFilesystem.commands.Start(ctx, request)
}
func (localFilesystem *LocalFilesystem) Wait(ctx context.Context, id, pattern string, offset int) (*agentmodel.CommandSnapshot, error) {
	return localFilesystem.commands.Wait(ctx, id, pattern, offset)
}
func (localFilesystem *LocalFilesystem) Cancel(ctx context.Context, id string) error {
	return localFilesystem.commands.Cancel(ctx, id)
}
func (localFilesystem *LocalFilesystem) Close(ctx context.Context) error {
	return localFilesystem.commands.Close(ctx)
}

var _ agentmodel.Filesystem = (*LocalFilesystem)(nil)
var _ agentmodel.CommandService = (*LocalFilesystem)(nil)
var _ agentmodel.ToolFilesystem = (*LocalFilesystem)(nil)
var _ patchFilesystem = (*LocalFilesystem)(nil)

func (localFilesystem *LocalFilesystem) resolvePath(path string) (string, error) {
	// 安全检查：禁止路径遍历
	cleanPath := filepath.Clean(path)
	if cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
		return "", agentmodel.ErrInvalidPath
	}

	// 处理相对路径
	var absolutePath string
	if filepath.IsAbs(path) {
		if localFilesystem.virtualMode {
			cleanAbsolutePath := filepath.Clean(path)
			if isPathWithinRoot(cleanAbsolutePath, localFilesystem.rootDir) {
				// Models may echo the absolute workspace path supplied in their
				// runtime context. Keep an already sandboxed path unchanged.
				absolutePath = cleanAbsolutePath
			} else {
				// Other absolute paths retain the virtual-root behavior: /etc/x
				// addresses <rootDir>/etc/x rather than the host filesystem.
				absolutePath = filepath.Join(localFilesystem.rootDir, strings.TrimLeft(cleanAbsolutePath, string(filepath.Separator)))
			}
		} else {
			absolutePath = path
		}
	} else {
		absolutePath = filepath.Join(localFilesystem.rootDir, path)
	}

	absolutePath = filepath.Clean(absolutePath)

	// 虚拟模式下，确保路径在 rootDir 下
	if localFilesystem.virtualMode {
		if !isPathWithinRoot(absolutePath, localFilesystem.rootDir) {
			return "", agentmodel.ErrInvalidPath
		}
	}

	return absolutePath, nil
}

func isPathWithinRoot(path, root string) bool {
	relativePath, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return relativePath == "." || (relativePath != ".." && !strings.HasPrefix(relativePath, ".."+string(filepath.Separator)))
}

func (localFilesystem *LocalFilesystem) ApplyPatch(ctx context.Context, patch string) (string, error) {
	return ApplyWorkspacePatch(ctx, localFilesystem, patch)
}

// globMaxResults glob 工具最大返回结果数
const globMaxResults = 1000

// isExcludedDirectory 判断是否跳过某些大型或无关目录
func isExcludedDirectory(name string) bool {
	switch name {
	case ".git", "node_modules", ".svn", ".hg", "__pycache__", ".tox", ".eggs", ".mypy_cache":
		return true
	}
	return false
}

// matchGlob 匹配 glob 模式，支持 ** 递归匹配
// pattern 和 name 都使用 / 作为分隔符
func matchGlob(pattern, name string) bool {
	// 统一使用 / 分隔符
	pattern = filepath.ToSlash(pattern)
	name = filepath.ToSlash(name)

	return matchGlobSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

// doGlobMatch 递归匹配 glob 模式的各段
func matchGlobSegments(patternParts, nameParts []string) bool {
	for len(patternParts) > 0 && len(nameParts) > 0 {
		patternPart := patternParts[0]

		if patternPart == "**" {
			// ** 可以匹配零个或多个路径段
			patternParts = patternParts[1:]
			if len(patternParts) == 0 {
				return true // ** 在末尾，匹配所有剩余路径
			}
			// 尝试 ** 匹配 0 到 N 个路径段
			for i := 0; i <= len(nameParts); i++ {
				if matchGlobSegments(patternParts, nameParts[i:]) {
					return true
				}
			}
			return false
		}

		// 使用 filepath.Match 匹配单个路径段
		matched, _ := filepath.Match(patternPart, nameParts[0])
		if !matched {
			return false
		}

		patternParts = patternParts[1:]
		nameParts = nameParts[1:]
	}

	// 处理 pattern 末尾的 **
	for _, patternPart := range patternParts {
		if patternPart != "**" {
			return false
		}
	}

	return len(nameParts) == 0
}

func (localFilesystem *LocalFilesystem) GetRoot() string { return localFilesystem.rootDir }

// Every file operation uses os.Root, so an intermediate symlink replacement
// cannot turn a previously validated path into a host filesystem access.
func (localFilesystem *LocalFilesystem) openRoot(ctx context.Context, path string) (*os.Root, string, error) {
	err := ctx.Err()
	if err != nil {
		return nil, "", err
	}
	absolutePath, err := localFilesystem.resolvePath(path)
	if err != nil {
		return nil, "", err
	}
	relativePath, err := filepath.Rel(localFilesystem.rootDir, absolutePath)
	if err != nil || !filepath.IsLocal(relativePath) {
		return nil, "", agentmodel.ErrInvalidPath
	}
	workspaceRoot, err := os.OpenRoot(localFilesystem.rootDir)
	if err != nil {
		return nil, "", err
	}
	return workspaceRoot, relativePath, nil
}
func (localFilesystem *LocalFilesystem) Resolve(ctx context.Context, path string, write bool) (string, error) {
	workspaceRoot, relativePath, err := localFilesystem.openRoot(ctx, path)
	if err != nil {
		return "", err
	}
	defer workspaceRoot.Close()
	existingPath := relativePath
	for {
		_, err = workspaceRoot.Stat(existingPath)
		if err == nil {
			break
		}
		if !write || !os.IsNotExist(err) || existingPath == "." {
			return "", err
		}
		existingPath = filepath.Dir(existingPath)
	}
	return filepath.Join(localFilesystem.rootDir, relativePath), nil
}
func (localFilesystem *LocalFilesystem) HasFile(ctx context.Context, path string) (bool, error) {
	workspaceRoot, relativePath, err := localFilesystem.openRoot(ctx, path)
	if err != nil {
		return false, err
	}
	defer workspaceRoot.Close()
	_, err = workspaceRoot.Lstat(relativePath)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}
func (localFilesystem *LocalFilesystem) readBytes(ctx context.Context, path string) ([]byte, error) {
	workspaceRoot, relativePath, err := localFilesystem.openRoot(ctx, path)
	if err != nil {
		return nil, err
	}
	defer workspaceRoot.Close()
	file, err := workspaceRoot.Open(relativePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !fileInfo.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", path)
	}
	maxContentBytes := int64(localFilesystem.maxFileSizeMB) << 20
	contentBytes, err := io.ReadAll(io.LimitReader(file, maxContentBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(contentBytes)) > maxContentBytes {
		return nil, fmt.Errorf("file exceeds %d MiB", localFilesystem.maxFileSizeMB)
	}
	err = ctx.Err()
	if err != nil {
		return nil, err
	}
	return contentBytes, nil
}
func (localFilesystem *LocalFilesystem) Read(ctx context.Context, path string, offset, limit *int) (string, error) {
	contentBytes, err := localFilesystem.readBytes(ctx, path)
	if err != nil {
		return "", err
	}
	return ReadFileLines(string(contentBytes), offset, limit), nil
}
func (localFilesystem *LocalFilesystem) Write(ctx context.Context, path, content string) (*agentmodel.WriteResult, error) {
	workspaceRoot, relativePath, err := localFilesystem.openRoot(ctx, path)
	if err != nil {
		return nil, err
	}
	defer workspaceRoot.Close()
	err = workspaceRoot.MkdirAll(filepath.Dir(relativePath), 0755)
	if err != nil {
		return nil, err
	}
	err = workspaceRoot.WriteFile(relativePath, []byte(content), 0644)
	if err != nil {
		return nil, err
	}
	return &agentmodel.WriteResult{Path: path}, nil
}
func (localFilesystem *LocalFilesystem) CreateFileNoReplace(ctx context.Context, path, content string) (*agentmodel.WriteResult, error) {
	workspaceRoot, relativePath, err := localFilesystem.openRoot(ctx, path)
	if err != nil {
		return nil, err
	}
	defer workspaceRoot.Close()
	err = workspaceRoot.MkdirAll(filepath.Dir(relativePath), 0755)
	if err != nil {
		return nil, err
	}
	parentRoot, err := workspaceRoot.OpenRoot(filepath.Dir(relativePath))
	if err != nil {
		return nil, err
	}
	defer parentRoot.Close()
	workspaceRoot = parentRoot
	relativePath = filepath.Base(relativePath)
	temporaryPath := ".patch-" + rand.Text()
	file, err := workspaceRoot.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return nil, err
	}
	defer workspaceRoot.Remove(temporaryPath)
	_, writeErr := file.WriteString(content)
	closeErr := file.Close()
	err = errors.Join(writeErr, closeErr, ctx.Err())
	if err != nil {
		return nil, err
	}
	err = workspaceRoot.Link(temporaryPath, relativePath)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("%w: %s", agentmodel.ErrAlreadyExists, path)
		}
		return nil, err
	}
	return &agentmodel.WriteResult{Path: path}, nil
}

func (localFilesystem *LocalFilesystem) Edit(ctx context.Context, path, oldText, newText string, replaceAll bool) (*agentmodel.EditResult, error) {
	if oldText == "" {
		return nil, fmt.Errorf("old text is required")
	}
	contentBytes, err := localFilesystem.readBytes(ctx, path)
	if err != nil {
		return nil, err
	}
	updatedContent, occurrences, err := ReplaceFileText(string(contentBytes), oldText, newText, replaceAll)
	if err != nil {
		return &agentmodel.EditResult{Path: path, Occurrences: occurrences}, err
	}
	_, err = localFilesystem.Write(ctx, path, updatedContent)
	if err != nil {
		return nil, err
	}
	return &agentmodel.EditResult{Path: path, Occurrences: occurrences}, nil
}
func (localFilesystem *LocalFilesystem) Delete(ctx context.Context, path string) (string, error) {
	workspaceRoot, relativePath, err := localFilesystem.openRoot(ctx, path)
	if err != nil {
		return "", err
	}
	defer workspaceRoot.Close()
	fileInfo, err := workspaceRoot.Lstat(relativePath)
	if os.IsNotExist(err) {
		return "File does not exist: " + path, nil
	}
	if err != nil {
		return "", err
	}
	if fileInfo.IsDir() {
		return "", fmt.Errorf("refusing to delete directory: %s", path)
	}
	err = workspaceRoot.Remove(relativePath)
	if err != nil {
		return "", err
	}
	return "Deleted file " + path, nil
}
func (localFilesystem *LocalFilesystem) List(ctx context.Context, path string) ([]agentmodel.FileInfo, error) {
	workspaceRoot, relativePath, err := localFilesystem.openRoot(ctx, path)
	if err != nil {
		return nil, err
	}
	defer workspaceRoot.Close()
	directory, err := workspaceRoot.Open(relativePath)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	fileInfos := make([]agentmodel.FileInfo, 0, len(entries))
	for _, entry := range entries {
		contextErr := ctx.Err()
		if contextErr != nil {
			return nil, contextErr
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return nil, err
		}
		fileInfos = append(fileInfos, agentmodel.FileInfo{Path: filepath.Join(path, entry.Name()), IsDir: entry.IsDir(), IsSymlink: entry.Type()&os.ModeSymlink != 0, Size: fileInfo.Size(), ModifiedAt: fileInfo.ModTime()})
	}
	sort.Slice(fileInfos, func(i, j int) bool {
		if fileInfos[i].IsDir != fileInfos[j].IsDir {
			return fileInfos[i].IsDir
		}
		return fileInfos[i].Path < fileInfos[j].Path
	})
	return fileInfos, nil
}
func (localFilesystem *LocalFilesystem) Glob(ctx context.Context, pattern, path string) ([]agentmodel.FileInfo, error) {
	workspaceRoot, relativePath, err := localFilesystem.openRoot(ctx, path)
	if err != nil {
		return nil, err
	}
	defer workspaceRoot.Close()
	var fileInfos []agentmodel.FileInfo
	err = fs.WalkDir(workspaceRoot.FS(), filepath.ToSlash(relativePath), func(entryPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		contextErr := ctx.Err()
		if contextErr != nil {
			return contextErr
		}
		if entryPath != relativePath && entry.IsDir() && isExcludedDirectory(entry.Name()) {
			return fs.SkipDir
		}
		relativeEntryPath, err := filepath.Rel(relativePath, entryPath)
		if err != nil {
			return err
		}
		if relativeEntryPath == "." || !matchGlob(pattern, relativeEntryPath) {
			return nil
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		fileInfos = append(fileInfos, agentmodel.FileInfo{Path: entryPath, IsDir: entry.IsDir(), IsSymlink: entry.Type()&os.ModeSymlink != 0, Size: fileInfo.Size(), ModifiedAt: fileInfo.ModTime()})
		if len(fileInfos) >= globMaxResults {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(fileInfos, func(i, j int) bool { return fileInfos[i].Path < fileInfos[j].Path })
	return fileInfos, nil
}
func (localFilesystem *LocalFilesystem) Grep(ctx context.Context, pattern, path, glob string) ([]agentmodel.GrepMatch, error) {
	patternRegexp, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	workspaceRoot, relativePath, err := localFilesystem.openRoot(ctx, path)
	if err != nil {
		return nil, err
	}
	defer workspaceRoot.Close()
	var grepMatches []agentmodel.GrepMatch
	err = fs.WalkDir(workspaceRoot.FS(), filepath.ToSlash(relativePath), func(entryPath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		contextErr := ctx.Err()
		if contextErr != nil {
			return contextErr
		}
		if entry.IsDir() {
			if entryPath != relativePath && isExcludedDirectory(entry.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if glob != "" && !matchGlob(glob, entry.Name()) && !matchGlob(glob, entryPath) {
			return nil
		}
		fileInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if !fileInfo.Mode().IsRegular() || fileInfo.Size() > int64(localFilesystem.maxFileSizeMB)<<20 {
			return nil
		}
		file, err := workspaceRoot.Open(entryPath)
		if err != nil {
			return err
		}
		defer file.Close()
		scanner := bufio.NewScanner(io.LimitReader(file, int64(localFilesystem.maxFileSizeMB)<<20))
		scanner.Buffer(make([]byte, 4096), 1<<20)
		lineNumber := 0
		for scanner.Scan() {
			contextErr := ctx.Err()
			if contextErr != nil {
				return contextErr
			}
			lineNumber++
			text := scanner.Text()
			if strings.ContainsRune(text, 0) {
				return nil
			}
			if patternRegexp.MatchString(text) {
				grepMatches = append(grepMatches, agentmodel.GrepMatch{Path: entryPath, Line: lineNumber, Text: text})
				if len(grepMatches) >= 100 {
					return fs.SkipAll
				}
			}
		}
		return scanner.Err()
	})
	return grepMatches, err
}
func (localFilesystem *LocalFilesystem) UploadFiles(ctx context.Context, files []struct {
	Path    string
	Content []byte
}) ([]agentmodel.FileUploadResponse, error) {
	uploadResponses := make([]agentmodel.FileUploadResponse, 0, len(files))
	for _, file := range files {
		contextErr := ctx.Err()
		if contextErr != nil {
			return nil, contextErr
		}
		writeResult, err := localFilesystem.Write(ctx, file.Path, string(file.Content))
		response := agentmodel.FileUploadResponse{Path: file.Path}
		if err != nil {
			response.Error = agentmodel.ErrInvalidPath
		} else if writeResult != nil {
			response.Error = writeResult.Error
		}
		uploadResponses = append(uploadResponses, response)
	}
	return uploadResponses, nil
}
func (localFilesystem *LocalFilesystem) DownloadFiles(ctx context.Context, paths []string) ([]agentmodel.FileDownloadResponse, error) {
	downloadResponses := make([]agentmodel.FileDownloadResponse, 0, len(paths))
	for _, path := range paths {
		contextErr := ctx.Err()
		if contextErr != nil {
			return nil, contextErr
		}
		contentBytes, err := localFilesystem.readBytes(ctx, path)
		response := agentmodel.FileDownloadResponse{Path: path, Content: contentBytes}
		if err != nil {
			response.Error = agentmodel.ErrInvalidPath
		}
		downloadResponses = append(downloadResponses, response)
	}
	return downloadResponses, nil
}
