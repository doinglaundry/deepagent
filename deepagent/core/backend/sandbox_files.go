package backend

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"sync"

	"eino-cli/deepagent/sandbox"
)

// SandboxFiles routes file operations to a provider-owned sandbox. It deliberately
// does not expose Workspace: a remote path must never become a host command cwd.
// The caller owns acquisition and release of the sandbox.
type SandboxFiles struct {
	sandbox sandbox.Sandbox
	mu      sync.RWMutex
	dir     string
}

func NewSandboxFiles(provider sandbox.Sandbox, dir string) (*SandboxFiles, error) {
	if provider == nil {
		return nil, fmt.Errorf("sandbox is required")
	}
	if !path.IsAbs(dir) || strings.ContainsRune(dir, 0) {
		return nil, fmt.Errorf("absolute sandbox working directory is required")
	}
	return &SandboxFiles{sandbox: provider, dir: path.Clean(dir)}, nil
}

func (b *SandboxFiles) resolve(ctx context.Context, name string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", sandboxFileError(ctx, err)
	}
	if strings.ContainsRune(name, 0) {
		return "", ErrInvalidPath
	}
	if path.IsAbs(name) {
		return path.Clean(name), nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	// Provider mounts and permissions enforce access, including symlinks.
	return path.Join(b.dir, name), nil
}

func sandboxFileInfos(paths []string) []FileInfo {
	out := make([]FileInfo, 0, len(paths))
	for _, name := range paths {
		out = append(out, FileInfo{Path: name, IsDir: strings.HasSuffix(name, "/")})
	}
	return out
}
func (b *SandboxFiles) LsInfo(ctx context.Context, name string) ([]FileInfo, error) {
	name, err := b.resolve(ctx, name)
	if err != nil {
		return nil, sandboxFileError(ctx, err)
	}
	entries, err := b.sandbox.ListDir(ctx, name, 1)
	if err != nil {
		return nil, sandboxFileError(ctx, err)
	}
	return sandboxFileInfos(entries), nil
}
func (b *SandboxFiles) Read(ctx context.Context, name string, offset, limit *int) (string, error) {
	name, err := b.resolve(ctx, name)
	if err != nil {
		return "", sandboxFileError(ctx, err)
	}
	content, err := b.sandbox.ReadFile(ctx, name)
	if err != nil {
		return "", sandboxFileError(ctx, err)
	}
	return ReadFileLines(content, offset, limit), nil
}
func (b *SandboxFiles) Write(ctx context.Context, name, content string) (*WriteResult, error) {
	resolved, err := b.resolve(ctx, name)
	if err != nil {
		return nil, sandboxFileError(ctx, err)
	}
	if err = b.sandbox.WriteFile(ctx, resolved, content, false); err != nil {
		return nil, sandboxFileError(ctx, err)
	}
	return &WriteResult{Path: name}, nil
}
func (b *SandboxFiles) Edit(ctx context.Context, name, old, new string, all bool) (*EditResult, error) {
	if old == "" {
		return nil, fmt.Errorf("old text is required")
	}
	resolved, err := b.resolve(ctx, name)
	if err != nil {
		return nil, sandboxFileError(ctx, err)
	}
	content, err := b.sandbox.ReadFile(ctx, resolved)
	if err != nil {
		return nil, sandboxFileError(ctx, err)
	}
	// Providers can complete a read after cancellation. Do not begin a second
	// remote operation once the caller has stopped this edit.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	updated, count, err := ReplaceFileText(content, old, new, all)
	if err != nil {
		return &EditResult{Path: name, Occurrences: count}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err = b.sandbox.WriteFile(ctx, resolved, updated, false); err != nil {
		return nil, sandboxFileError(ctx, err)
	}
	return &EditResult{Path: name, Occurrences: count}, nil
}
func (b *SandboxFiles) GrepRaw(ctx context.Context, pattern, name, glob string) ([]GrepMatch, error) {
	name, err := b.resolve(ctx, name)
	if err != nil {
		return nil, sandboxFileError(ctx, err)
	}
	matches, _, err := b.sandbox.Grep(ctx, name, pattern, sandbox.GrepOpts{Glob: glob, CaseSensitive: true, MaxResults: 100})
	if err != nil {
		return nil, sandboxFileError(ctx, err)
	}
	out := make([]GrepMatch, 0, len(matches))
	for _, m := range matches {
		out = append(out, GrepMatch{Path: m.Path, Line: m.LineNumber, Text: m.Line})
	}
	return out, nil
}
func (b *SandboxFiles) GlobInfo(ctx context.Context, pattern, name string) ([]FileInfo, error) {
	name, err := b.resolve(ctx, name)
	if err != nil {
		return nil, sandboxFileError(ctx, err)
	}
	entries, _, err := b.sandbox.Glob(ctx, name, pattern, sandbox.GlobOpts{MaxResults: 200})
	if err != nil {
		return nil, sandboxFileError(ctx, err)
	}
	return sandboxFileInfos(entries), nil
}
func (b *SandboxFiles) UploadFiles(ctx context.Context, files []struct {
	Path    string
	Content []byte
}) ([]FileUploadResponse, error) {
	out := make([]FileUploadResponse, 0, len(files))
	for _, file := range files {
		name, err := b.resolve(ctx, file.Path)
		if err != nil {
			return out, sandboxFileError(ctx, err)
		}
		if err = b.sandbox.UpdateFile(ctx, name, file.Content); err != nil {
			return out, sandboxFileError(ctx, err)
		}
		out = append(out, FileUploadResponse{Path: file.Path})
	}
	return out, nil
}
func (b *SandboxFiles) ChangeDir(ctx context.Context, name string) error {
	name, err := b.resolve(ctx, name)
	if err != nil {
		return sandboxFileError(ctx, err)
	}
	if _, err = b.sandbox.ListDir(ctx, name, 1); err != nil {
		return sandboxFileError(ctx, err)
	}
	b.mu.Lock()
	b.dir = name
	b.mu.Unlock()
	return nil
}

var _ Backend = (*SandboxFiles)(nil)

// Older providers wrap transport failures as text. Retain their details while
// preserving cancellation identity for the Graph's system-error boundary.
func sandboxFileError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), err)
	}
	return err
}
