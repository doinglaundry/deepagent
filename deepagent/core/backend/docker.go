package backend

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"strings"
	"sync"

	"eino-cli/deepagent/sandbox"
)

// DockerFilesystem keeps file and command operations in the same container.
// The caller owns acquisition and release of the sandbox.
type DockerFilesystem struct {
	sandbox       sandbox.Sandbox
	mu            sync.RWMutex
	dir           string
	root          string
	containerID   string
	pathResolver  sandbox.ContainerPathResolver
	canonicalRoot string
	commands      *Commands
}

func NewDockerFilesystem(provider sandbox.Sandbox, dir, threadID string) (*DockerFilesystem, error) {
	if provider == nil {
		return nil, fmt.Errorf("sandbox is required")
	}
	if !path.IsAbs(dir) || strings.ContainsRune(dir, 0) {
		return nil, fmt.Errorf("absolute sandbox working directory is required")
	}
	target, ok := provider.(interface{ DockerExecTarget() (string, bool) })
	if !ok {
		return nil, fmt.Errorf("sandbox does not expose a Docker container")
	}
	containerID, ok := target.DockerExecTarget()
	if !ok || containerID == "" || threadID == "" {
		return nil, fmt.Errorf("Docker container and thread ID are required")
	}
	resolver, ok := provider.(sandbox.ContainerPathResolver)
	if !ok {
		return nil, fmt.Errorf("Docker sandbox must resolve container paths")
	}
	b := &DockerFilesystem{sandbox: provider, dir: path.Clean(dir), root: path.Clean(dir), containerID: containerID, pathResolver: resolver}
	b.commands = NewDockerCommands(threadID, b, containerID)
	return b, nil
}

func (b *DockerFilesystem) resolve(ctx context.Context, name string) (string, error) {
	err := ctx.Err()
	if err != nil {
		return "", dockerFileError(ctx, err)
	}
	if strings.ContainsRune(name, 0) {
		return "", ErrInvalidPath
	}
	b.mu.RLock()
	dir := b.dir
	b.mu.RUnlock()
	if name == "" {
		name = "."
	}
	resolved := path.Clean(name)
	if !path.IsAbs(resolved) {
		resolved = path.Join(dir, resolved)
	}
	if resolved != b.root && b.root != "/" && !strings.HasPrefix(resolved, b.root+"/") {
		return "", ErrInvalidPath
	}
	b.mu.RLock()
	canonicalRoot := b.canonicalRoot
	b.mu.RUnlock()
	if canonicalRoot == "" {
		var err error
		canonicalRoot, err = b.pathResolver.ResolveContainerPath(ctx, b.root)
		if err != nil {
			return "", err
		}
		if !path.IsAbs(canonicalRoot) || path.Clean(canonicalRoot) != canonicalRoot {
			return "", ErrInvalidPath
		}
		b.mu.Lock()
		if b.canonicalRoot == "" {
			b.canonicalRoot = canonicalRoot
		} else {
			canonicalRoot = b.canonicalRoot
		}
		b.mu.Unlock()
	}
	canonical, err := b.pathResolver.ResolveContainerPath(ctx, resolved)
	if err != nil {
		return "", err
	}
	if !path.IsAbs(canonical) || path.Clean(canonical) != canonical {
		return "", ErrInvalidPath
	}
	if canonical != canonicalRoot && canonicalRoot != "/" && !strings.HasPrefix(canonical, canonicalRoot+"/") {
		return "", ErrInvalidPath
	}
	return resolved, nil
}

func (b *DockerFilesystem) Root() string { return b.root }
func (b *DockerFilesystem) Resolve(ctx context.Context, name string, _ bool) (string, error) {
	return b.resolve(ctx, name)
}
func (b *DockerFilesystem) Execute(ctx context.Context, req CommandRequest) (*CommandResult, error) {
	return b.commands.Execute(ctx, req)
}
func (b *DockerFilesystem) Start(ctx context.Context, req CommandRequest) (string, error) {
	return b.commands.Start(ctx, req)
}
func (b *DockerFilesystem) Wait(ctx context.Context, id, pattern string, offset int) (*CommandSnapshot, error) {
	return b.commands.Wait(ctx, id, pattern, offset)
}
func (b *DockerFilesystem) Cancel(ctx context.Context, id string) error {
	return b.commands.Cancel(ctx, id)
}
func (b *DockerFilesystem) Close(ctx context.Context) error { return b.commands.Close(ctx) }

func dockerFileInfos(paths []string) []FileInfo {
	out := make([]FileInfo, 0, len(paths))
	for _, name := range paths {
		out = append(out, FileInfo{Path: name, IsDir: strings.HasSuffix(name, "/")})
	}
	return out
}
func (b *DockerFilesystem) List(ctx context.Context, name string) ([]FileInfo, error) {
	name, err := b.resolve(ctx, name)
	if err != nil {
		return nil, dockerFileError(ctx, err)
	}
	provider, ok := b.sandbox.(sandbox.FileInfoProvider)
	if ok {
		entries, err := provider.ListDirInfo(ctx, name, 1)
		if err != nil {
			return nil, dockerFileError(ctx, err)
		}
		out := make([]FileInfo, 0, len(entries))
		for _, entry := range entries {
			out = append(out, FileInfo{Path: entry.Path, IsDir: entry.IsDir, IsSymlink: entry.IsSymlink, Size: entry.Size})
		}
		return out, nil
	}
	entries, err := b.sandbox.ListDir(ctx, name, 1)
	if err != nil {
		return nil, dockerFileError(ctx, err)
	}
	return dockerFileInfos(entries), nil
}
func (b *DockerFilesystem) Read(ctx context.Context, name string, offset, limit *int) (string, error) {
	name, err := b.resolve(ctx, name)
	if err != nil {
		return "", dockerFileError(ctx, err)
	}
	content, err := b.sandbox.ReadFile(ctx, name)
	if err != nil {
		return "", dockerFileError(ctx, err)
	}
	if len(content) > MaxFileSizeMB<<20 {
		return "", fmt.Errorf("file exceeds %d MiB", MaxFileSizeMB)
	}
	return ReadFileLines(content, offset, limit), nil
}
func (b *DockerFilesystem) Write(ctx context.Context, name, content string) (*WriteResult, error) {
	resolved, err := b.resolve(ctx, name)
	if err != nil {
		return nil, dockerFileError(ctx, err)
	}
	err = b.sandbox.WriteFile(ctx, resolved, content, false)
	if err != nil {
		return nil, dockerFileError(ctx, err)
	}
	return &WriteResult{Path: name}, nil
}
func (b *DockerFilesystem) Edit(ctx context.Context, name, old, new string, all bool) (*EditResult, error) {
	if old == "" {
		return nil, fmt.Errorf("old text is required")
	}
	resolved, err := b.resolve(ctx, name)
	if err != nil {
		return nil, dockerFileError(ctx, err)
	}
	content, err := b.sandbox.ReadFile(ctx, resolved)
	if err != nil {
		return nil, dockerFileError(ctx, err)
	}
	if len(content) > MaxFileSizeMB<<20 {
		return nil, fmt.Errorf("file exceeds %d MiB", MaxFileSizeMB)
	}
	// Providers can complete a read after cancellation. Do not begin a second
	// remote operation once the caller has stopped this edit.
	contextErr := ctx.Err()
	if contextErr != nil {
		return nil, contextErr
	}
	updated, count, err := ReplaceFileText(content, old, new, all)
	if err != nil {
		return &EditResult{Path: name, Occurrences: count}, err
	}
	contextErr = ctx.Err()
	if contextErr != nil {
		return nil, contextErr
	}
	err = b.sandbox.WriteFile(ctx, resolved, updated, false)
	if err != nil {
		return nil, dockerFileError(ctx, err)
	}
	return &EditResult{Path: name, Occurrences: count}, nil
}
func (b *DockerFilesystem) Grep(ctx context.Context, pattern, name, glob string) ([]GrepMatch, error) {
	name, err := b.resolve(ctx, name)
	if err != nil {
		return nil, dockerFileError(ctx, err)
	}
	matches, _, err := b.sandbox.Grep(ctx, name, pattern, sandbox.GrepOpts{Glob: glob, CaseSensitive: true, MaxResults: 100})
	if err != nil {
		return nil, dockerFileError(ctx, err)
	}
	out := make([]GrepMatch, 0, len(matches))
	for _, m := range matches {
		out = append(out, GrepMatch{Path: m.Path, Line: m.LineNumber, Text: m.Line})
	}
	return out, nil
}
func (b *DockerFilesystem) Glob(ctx context.Context, pattern, name string) ([]FileInfo, error) {
	name, err := b.resolve(ctx, name)
	if err != nil {
		return nil, dockerFileError(ctx, err)
	}
	entries, _, err := b.sandbox.Glob(ctx, name, pattern, sandbox.GlobOpts{MaxResults: globMaxResults})
	if err != nil {
		return nil, dockerFileError(ctx, err)
	}
	return dockerFileInfos(entries), nil
}
func (b *DockerFilesystem) UploadFiles(ctx context.Context, files []struct {
	Path    string
	Content []byte
}) ([]FileUploadResponse, error) {
	out := make([]FileUploadResponse, 0, len(files))
	for _, file := range files {
		name, err := b.resolve(ctx, file.Path)
		if err != nil {
			return out, dockerFileError(ctx, err)
		}
		err = b.sandbox.UpdateFile(ctx, name, file.Content)
		if err != nil {
			return out, dockerFileError(ctx, err)
		}
		out = append(out, FileUploadResponse{Path: file.Path})
	}
	return out, nil
}
func (b *DockerFilesystem) ChangeDir(ctx context.Context, name string) error {
	name, err := b.resolve(ctx, name)
	if err != nil {
		return dockerFileError(ctx, err)
	}
	_, err = b.sandbox.ListDir(ctx, name, 1)
	if err != nil {
		return dockerFileError(ctx, err)
	}
	b.mu.Lock()
	b.dir = name
	b.mu.Unlock()
	return nil
}

func (b *DockerFilesystem) Delete(ctx context.Context, name string) (string, error) {
	resolved, err := b.resolve(ctx, name)
	if err != nil {
		return "", err
	}
	if resolved == b.root {
		return "", ErrInvalidPath
	}
	cmd := exec.CommandContext(ctx, "docker", "exec", b.containerID, "rm", "-f", "--", resolved)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("docker delete %s: %s: %w", name, strings.TrimSpace(string(output)), err)
	}
	return "Deleted file " + name, nil
}

func (b *DockerFilesystem) ApplyPatch(ctx context.Context, patch string) (string, error) {
	return ApplyWorkspacePatch(ctx, b, patch)
}

var _ Filesystem = (*DockerFilesystem)(nil)
var _ CommandService = (*DockerFilesystem)(nil)
var _ ToolFilesystem = (*DockerFilesystem)(nil)

// Older providers wrap transport failures as text. Retain their details while
// preserving cancellation identity for the Graph's system-error boundary.
func dockerFileError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), err)
	}
	return err
}
