package backend

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

type Filesystem interface {
	Root() string
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

// ToolWorkspace is the complete capability set exposed to one Agent thread.
// Both concrete filesystems implement it; tools never inspect optional backend types.
type ToolWorkspace interface {
	Filesystem
	CommandService
}

func (b *LocalFilesystem) Root() string { return b.rootDir }
func (b *LocalFilesystem) List(ctx context.Context, path string) ([]FileInfo, error) {
	return b.LsInfo(ctx, path)
}
func (b *LocalFilesystem) Delete(ctx context.Context, path string) (string, error) {
	return b.DeleteFile(ctx, path)
}
func (b *LocalFilesystem) Glob(ctx context.Context, pattern, path string) ([]FileInfo, error) {
	return b.GlobInfo(ctx, pattern, path)
}
func (b *LocalFilesystem) Grep(ctx context.Context, pattern, path, glob string) ([]GrepMatch, error) {
	return b.GrepRaw(ctx, pattern, path, glob)
}

// Every file operation uses os.Root, so an intermediate symlink replacement
// cannot turn a previously validated path into a host filesystem access.
func (b *LocalFilesystem) openRoot(ctx context.Context, path string) (*os.Root, string, error) {
	if err := ctx.Err(); err != nil {
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
	if err := ctx.Err(); err != nil {
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
	if err := root.MkdirAll(filepath.Dir(relative), 0755); err != nil {
		return nil, err
	}
	if err := root.WriteFile(relative, []byte(content), 0644); err != nil {
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
	if _, err := b.Write(ctx, path, updated); err != nil {
		return nil, err
	}
	return &EditResult{Path: path, Occurrences: count}, nil
}
func (b *LocalFilesystem) DeleteFile(ctx context.Context, path string) (string, error) {
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
	if err := root.Remove(relative); err != nil {
		return "", err
	}
	return "Deleted file " + path, nil
}
func (b *LocalFilesystem) LsInfo(ctx context.Context, path string) ([]FileInfo, error) {
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
		if err := ctx.Err(); err != nil {
			return nil, err
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
func (b *LocalFilesystem) GlobInfo(ctx context.Context, pattern, path string) ([]FileInfo, error) {
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
		if err := ctx.Err(); err != nil {
			return err
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
func (b *LocalFilesystem) GrepRaw(ctx context.Context, pattern, path, glob string) ([]GrepMatch, error) {
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
		if err := ctx.Err(); err != nil {
			return err
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
			if err := ctx.Err(); err != nil {
				return err
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
		if err := ctx.Err(); err != nil {
			return nil, err
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
		if err := ctx.Err(); err != nil {
			return nil, err
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

var _ Filesystem = (*LocalFilesystem)(nil)
