// Package uploads owns the per-session file upload directory with traversal/symlink guards.
package uploads

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"eino-cli/deepagent/config"
)

// ErrPathTraversal signals a destination outside the session's uploads dir.
var ErrPathTraversal = errors.New("path traversal detected")

// ErrUnsafeFilename signals an empty / "." / ".." / overlong / backslash filename.
var ErrUnsafeFilename = errors.New("unsafe filename")

// ValidateSessionID rejects session ids that would escape the sessions/ tree.
func ValidateSessionID(sessionID string) error {
	return config.ValidateSessionID(sessionID)
}

// NormalizeFilename strips the directory part and rejects traversal-shaped names.
func NormalizeFilename(filename string) (string, error) {
	if filename == "" {
		return "", ErrUnsafeFilename
	}
	if strings.ContainsRune(filename, '\\') {
		return "", fmt.Errorf("%w: backslash", ErrUnsafeFilename)
	}
	safe := filepath.Base(filename)
	if safe == "." || safe == ".." || safe == "" {
		return "", ErrUnsafeFilename
	}
	if len(safe) > 255 {
		return "", fmt.Errorf("%w: too long", ErrUnsafeFilename)
	}
	return safe, nil
}

// PathFor returns the host path for filename under sessionID without creating anything.
func PathFor(sessionID, filename string) (string, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return "", err
	}
	safe, err := NormalizeFilename(filename)
	if err != nil {
		return "", err
	}
	base := config.SandboxUploadsDir(sessionID)
	dest := filepath.Join(base, safe)
	if err := guardTraversal(dest, base); err != nil {
		return "", err
	}
	return dest, nil
}

// Write streams src into uploads/<filename> with O_NOFOLLOW; returns the host path.
func Write(sessionID, filename string, src io.Reader) (string, error) {
	dest, err := PathFor(sessionID, filename)
	if err != nil {
		return "", err
	}
	root, err := openUploadsRoot(sessionID, true)
	if err != nil {
		return "", err
	}
	defer root.Close()
	safe := filepath.Base(dest)
	err = rejectUploadSymlink(root, safe)
	if err != nil {
		return "", err
	}
	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC | osNoFollow()
	f, err := root.OpenFile(safe, flag, 0o600)
	if err != nil {
		return "", fmt.Errorf("uploads: open %s: %w", dest, err)
	}
	defer f.Close()
	if _, err := io.Copy(f, src); err != nil {
		return "", fmt.Errorf("uploads: write %s: %w", dest, err)
	}
	return dest, nil
}

// FileInfo is the per-file struct List returns.
type FileInfo struct {
	Filename  string
	Size      int64
	Path      string
	Extension string
	Modified  int64
}

// List enumerates regular files in the session's uploads dir, sorted by name; symlinks are skipped.
func List(sessionID string) ([]FileInfo, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, err
	}
	base := config.SandboxUploadsDir(sessionID)
	root, err := openUploadsRoot(sessionID, false)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer root.Close()
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var out []FileInfo
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		out = append(out, FileInfo{
			Filename:  e.Name(),
			Size:      info.Size(),
			Path:      filepath.Join(base, e.Name()),
			Extension: filepath.Ext(e.Name()),
			Modified:  info.ModTime().Unix(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Filename < out[j].Filename })
	return out, nil
}

// Delete removes a single file with the same safety profile as Write; already-gone is fine.
func Delete(sessionID, filename string) error {
	dest, err := PathFor(sessionID, filename)
	if err != nil {
		return err
	}
	root, err := openUploadsRoot(sessionID, false)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	safe := filepath.Base(dest)
	info, err := root.Lstat(safe)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafeFilename
	}
	return root.Remove(safe)
}

func openUploadsRoot(sessionID string, create bool) (*os.Root, error) {
	if create {
		err := config.EnsureSessionDirs(sessionID)
		if err != nil {
			return nil, err
		}
	}
	sessionRoot, err := config.OpenSessionDir(sessionID)
	if err != nil {
		return nil, err
	}
	info, err := sessionRoot.Lstat("uploads")
	if err != nil {
		_ = sessionRoot.Close()
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		_ = sessionRoot.Close()
		return nil, fmt.Errorf("refusing symlinked uploads directory")
	}
	if !info.IsDir() {
		_ = sessionRoot.Close()
		return nil, fmt.Errorf("uploads path is not a directory")
	}
	root, err := sessionRoot.OpenRoot("uploads")
	_ = sessionRoot.Close()
	if err != nil {
		return nil, err
	}
	openedInfo, err := root.Stat(".")
	if err != nil || !os.SameFile(info, openedInfo) {
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("uploads directory changed while opening")
	}
	return root, nil
}

func rejectUploadSymlink(root *os.Root, filename string) error {
	info, err := root.Lstat(filename)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafeFilename
	}
	return nil
}

func guardTraversal(dest, base string) error {
	absBase, err := filepath.Abs(base)
	if err != nil {
		return err
	}
	absDest, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if absDest != absBase && !strings.HasPrefix(absDest, absBase+string(filepath.Separator)) {
		return ErrPathTraversal
	}
	return nil
}
