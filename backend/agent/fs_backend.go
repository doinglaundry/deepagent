// ignore_security_alert_file RCE
package agent

import (
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk/filesystem"
)

// This file owns the on-host implementations of the deep agent's tool
// surface — file ops (localBackend), shell exec (localShell), and image
// reading (readImage). Previously these lived behind a SandboxProvider
// interface ("local / Docker / aio-sandbox / ACP" pluggable hosts), but
// only the local impl was ever wired into eino-cli, so the abstraction
// was deleted alongside this rename. If a second host appears, restore
// the interface; until then everything here is just plain types and
// functions used directly by MakeLeadAgent / BuildChain.

// localBackend implements filesystem.Backend (Read / Write / Edit / Ls /
// Grep / Glob) against a real filesystem rooted at root. Relative paths
// in requests resolve under root; absolute paths are honoured as-is.
type localBackend struct{ root string }

// localShell implements filesystem.Shell by exec'ing /bin/bash with the
// agent's logical CWD.
type localShell struct{ cwd string }

// resolveRoot mirrors the old NewLocalSandbox("") fallback: empty input
// → os.Getwd() → "." as the last resort. Both backend and shell pass
// the same value, so they always agree on what "relative" means.
func resolveRoot(root string) string {
	if strings.TrimSpace(root) != "" {
		return root
	}
	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		return cwd
	}
	return "."
}

func newLocalBackend(root string) *localBackend { return &localBackend{root: resolveRoot(root)} }
func newLocalShell(cwd string) *localShell      { return &localShell{cwd: resolveRoot(cwd)} }

// readImage resolves path against root, refuses non-regular files,
// infers the MIME type via the standard library's content sniffer
// (with an extension fallback), and returns the raw bytes ready for
// base64 encoding into a multimodal message. Empty root falls back to
// cwd via resolveRoot.
//
// This used to be LocalSandboxProvider.ReadImage — it became a plain
// function once the SandboxProvider abstraction was deleted.
// middleware_chain.go wraps it in an imageFetcherFunc adapter to
// satisfy the ViewImage middleware's ImageFetcher interface.
func readImage(ctx context.Context, root, path string) ([]byte, string, error) {
	if strings.TrimSpace(path) == "" {
		return nil, "", fmt.Errorf("read image: path is empty")
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(resolveRoot(root), abs)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, "", fmt.Errorf("read image: stat %s: %w", abs, err)
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("read image: %s is not a regular file", abs)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, "", fmt.Errorf("read image: %s: %w", abs, err)
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("read image: %s is empty", abs)
	}

	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		// Fall back to the file extension when DetectContentType returned
		// "application/octet-stream" or similar — some tiny SVG / WebP
		// inputs trip the sniffer.
		if extMime := mimeFromExt(abs); extMime != "" {
			mime = extMime
		}
	}
	if !strings.HasPrefix(mime, "image/") {
		return nil, "", fmt.Errorf("read image: %s is not an image (mime=%s)", abs, mime)
	}
	return data, mime, nil
}

// imageFetcherFunc adapts a plain ReadImage-shaped function into the
// middlewares.ImageFetcher interface (declared in
// agent/middlewares/view_image.go). BuildChain constructs one inline
// closing over readImage + the cwd root.
type imageFetcherFunc func(ctx context.Context, path string) ([]byte, string, error)

func (f imageFetcherFunc) ReadImage(ctx context.Context, path string) ([]byte, string, error) {
	return f(ctx, path)
}

// mimeFromExt returns a best-effort image MIME type for known file
// extensions. Returns "" when the extension isn't a known image format
// so callers know to keep the sniffer's verdict.
func mimeFromExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".svg":
		return "image/svg+xml"
	case ".heic":
		return "image/heic"
	case ".heif":
		return "image/heif"
	default:
		return ""
	}
}

// -----------------------------------------------------------------------------
// localBackend — filesystem.Backend implementation
// -----------------------------------------------------------------------------

func (b *localBackend) LsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	dir := req.Path
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(b.root, dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	result := make([]filesystem.FileInfo, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		result = append(result, filesystem.FileInfo{
			Path:       e.Name(),
			IsDir:      e.IsDir(),
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	return result, nil
}

func (b *localBackend) Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	p := req.FilePath
	if !filepath.IsAbs(p) {
		p = filepath.Join(b.root, p)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	content := string(data)
	if req.Offset > 0 || req.Limit > 0 {
		lines := strings.Split(content, "\n")
		start := req.Offset - 1
		if start < 0 {
			start = 0
		}
		if start >= len(lines) {
			return &filesystem.FileContent{Content: ""}, nil
		}
		end := len(lines)
		if req.Limit > 0 && start+req.Limit < end {
			end = start + req.Limit
		}
		content = strings.Join(lines[start:end], "\n")
	}
	return &filesystem.FileContent{Content: content}, nil
}

func (b *localBackend) GrepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	pattern := req.Pattern
	if req.CaseInsensitive {
		pattern = "(?i:" + pattern + ")"
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid pattern: %w", err)
	}

	searchRoot := b.root
	if req.Path != "" {
		if filepath.IsAbs(req.Path) {
			searchRoot = req.Path
		} else {
			searchRoot = filepath.Join(b.root, req.Path)
		}
	}

	var matches []filesystem.GrepMatch
	err = filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		rel, _ := filepath.Rel(b.root, path)
		for i, line := range strings.Split(string(data), "\n") {
			if re.MatchString(line) {
				matches = append(matches, filesystem.GrepMatch{
					Path:    rel,
					Line:    i + 1,
					Content: line,
				})
			}
		}
		return nil
	})
	return matches, err
}

func (b *localBackend) GlobInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	base := b.root
	if req.Path != "" {
		if filepath.IsAbs(req.Path) {
			base = req.Path
		} else {
			base = filepath.Join(b.root, req.Path)
		}
	}
	pattern := filepath.Join(base, req.Pattern)
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, err
	}
	result := make([]filesystem.FileInfo, 0, len(paths))
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(b.root, p)
		result = append(result, filesystem.FileInfo{
			Path:       rel,
			IsDir:      info.IsDir(),
			Size:       info.Size(),
			ModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	return result, nil
}

func (b *localBackend) Write(ctx context.Context, req *filesystem.WriteRequest) error {
	p := req.FilePath
	if !filepath.IsAbs(p) {
		p = filepath.Join(b.root, p)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(req.Content), 0644)
}

func (b *localBackend) Edit(ctx context.Context, req *filesystem.EditRequest) error {
	p := req.FilePath
	if !filepath.IsAbs(p) {
		p = filepath.Join(b.root, p)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	content := string(data)
	if req.OldString == "" {
		return fmt.Errorf("old_string must not be empty")
	}
	if !req.ReplaceAll {
		count := strings.Count(content, req.OldString)
		if count == 0 {
			return fmt.Errorf("old_string not found in file")
		}
		if count > 1 {
			return fmt.Errorf("old_string appears %d times; set replace_all=true or make it unique", count)
		}
	}
	n := 1
	if req.ReplaceAll {
		n = -1
	}
	newContent := strings.Replace(content, req.OldString, req.NewString, n)
	return os.WriteFile(p, []byte(newContent), 0644)
}

// -----------------------------------------------------------------------------
// localShell — filesystem.Shell implementation
// -----------------------------------------------------------------------------

func (s *localShell) Execute(ctx context.Context, input *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	cmd := exec.CommandContext(ctx, "bash", "-lc", input.Command)
	cmd.Dir = s.cwd
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, err
		}
	}
	return &filesystem.ExecuteResponse{Output: string(out), ExitCode: &exitCode}, nil
}
