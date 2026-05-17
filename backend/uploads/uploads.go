// Package uploads owns the per-thread file upload directory. Mirrors
// deerflow.uploads.manager — filename normalisation, traversal guard,
// O_NOFOLLOW write — minus the FastAPI bits that don't apply to a Go
// service.
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

// ErrPathTraversal: destination resolved outside the thread's uploads
// directory. Caller's job to map this to a 4xx in the gateway.
var ErrPathTraversal = errors.New("path traversal detected")

// ErrUnsafeFilename: empty / "." / ".." / overlong / contains backslash.
var ErrUnsafeFilename = errors.New("unsafe filename")

// safeThreadID: alphanumeric + `_`, `-`, `.` only. Stops the model (or a
// confused gateway client) from injecting `..` into the thread segment.
var safeThreadID = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// ValidateThreadID rejects any tid that would escape the threads/ tree.
func ValidateThreadID(tid string) error {
	if tid == "" {
		return fmt.Errorf("invalid thread_id: empty")
	}
	if !safeThreadID.MatchString(tid) {
		return fmt.Errorf("invalid thread_id: %q", tid)
	}
	return nil
}

// NormalizeFilename strips any directory part and rejects traversal-
// shaped names. Returns the bare basename when safe.
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

// PathFor returns the host path for `filename` under (tid, uid)'s uploads
// dir without creating anything. Use Write to actually persist data.
func PathFor(cfg *config.Config, tid, uid, filename string) (string, error) {
	if err := ValidateThreadID(tid); err != nil {
		return "", err
	}
	safe, err := NormalizeFilename(filename)
	if err != nil {
		return "", err
	}
	base := config.SandboxUploadsDir(cfg, tid, uid)
	dest := filepath.Join(base, safe)
	if err := guardTraversal(dest, base); err != nil {
		return "", err
	}
	return dest, nil
}

// Write streams `src` into uploads/<filename>, refusing to follow a
// pre-existing symlink at the destination (POSIX O_NOFOLLOW). Returns
// the host path on success so callers can hand it to the agent.
func Write(cfg *config.Config, tid, uid, filename string, src io.Reader) (string, error) {
	dest, err := PathFor(cfg, tid, uid, filename)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	// O_NOFOLLOW guards against a malicious sandbox process planting a
	// symlink at the dest path; the upload process owns more privilege
	// than the sandboxed agent. Linux/macOS both honour this flag.
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

// FileInfo is the per-file struct List returns. Sizes stay int64 here;
// gateway serializers can stringify for JSON if they want.
type FileInfo struct {
	Filename  string
	Size      int64
	Path      string
	Extension string
	Modified  int64 // unix seconds; stays an integer for JSON friendliness
}

// List enumerates regular files in the thread's uploads dir, sorted by
// name. Symlinks are skipped — same rule as Write enforces on the way in.
func List(cfg *config.Config, tid, uid string) ([]FileInfo, error) {
	if err := ValidateThreadID(tid); err != nil {
		return nil, err
	}
	base := config.SandboxUploadsDir(cfg, tid, uid)
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

// Delete removes a single file. Mirrors the safety profile of Write —
// traversal-guarded resolve, refuses symlinks, no error on already-gone.
func Delete(cfg *config.Config, tid, uid, filename string) error {
	dest, err := PathFor(cfg, tid, uid, filename)
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

// guardTraversal: dest must resolve under base. We use filepath.Abs
// because os.Lstat / Stat on a not-yet-created dest can fail, but the
// path-cleaning is enough for the prefix check.
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
