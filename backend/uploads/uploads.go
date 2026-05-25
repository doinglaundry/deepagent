// Package uploads owns the per-session file upload directory with traversal/symlink guards.
package uploads

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"eino-cli/backend/config"
)

// ErrPathTraversal signals a destination outside the session's uploads dir.
var ErrPathTraversal = errors.New("path traversal detected")

// ErrUnsafeFilename signals an empty / "." / ".." / overlong / backslash filename.
var ErrUnsafeFilename = errors.New("unsafe filename")

var safeSessionID = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// ValidateSessionID rejects session ids that would escape the sessions/ tree.
func ValidateSessionID(sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("invalid session_id: empty")
	}
	if !safeSessionID.MatchString(sessionID) {
		return fmt.Errorf("invalid session_id: %q", sessionID)
	}
	return nil
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

// PathFor returns the host path for filename under (sessionID, uid) without creating anything.
func PathFor(sessionID, uid, filename string) (string, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return "", err
	}
	safe, err := NormalizeFilename(filename)
	if err != nil {
		return "", err
	}
	base := config.SandboxUploadsDir(sessionID, uid)
	dest := filepath.Join(base, safe)
	if err := guardTraversal(dest, base); err != nil {
		return "", err
	}
	return dest, nil
}

// Write streams src into uploads/<filename> with O_NOFOLLOW; returns the host path.
func Write(sessionID, uid, filename string, src io.Reader) (string, error) {
	dest, err := PathFor(sessionID, uid, filename)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC | osNoFollow()
	f, err := os.OpenFile(dest, flag, 0o600)
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
func List(sessionID, uid string) ([]FileInfo, error) {
	if err := ValidateSessionID(sessionID); err != nil {
		return nil, err
	}
	base := config.SandboxUploadsDir(sessionID, uid)
	entries, err := os.ReadDir(base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []FileInfo
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
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
func Delete(sessionID, uid, filename string) error {
	dest, err := PathFor(sessionID, uid, filename)
	if err != nil {
		return err
	}
	info, err := os.Lstat(dest)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrUnsafeFilename
	}
	return os.Remove(dest)
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
