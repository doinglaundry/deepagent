package filesystem

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path"
	"strings"
	"sync"

	agentmodel "eino-cli/deepagent/model"
)

// DockerFilesystem keeps file and command operations in the same container.
// When releaseContainer is provided, the filesystem owns the container,
// including cleanup on construction failure.
type DockerFilesystem struct {
	releaseContainer func()
	releaseOnce      sync.Once
	sandbox          agentmodel.Sandbox
	mu               sync.RWMutex
	dir              string
	root             string
	containerID      string
	pathResolver     agentmodel.ContainerPathResolver
	canonicalRoot    string
	commands         *Commands
}

func NewDockerFilesystem(provider agentmodel.Sandbox, workDir, threadID string, releaseContainer func()) (filesystem *DockerFilesystem, err error) {
	defer func() {
		if filesystem == nil && releaseContainer != nil {
			releaseContainer()
		}
	}()
	if provider == nil {
		return nil, fmt.Errorf("sandbox is required")
	}
	if !path.IsAbs(workDir) || strings.ContainsRune(workDir, 0) {
		return nil, fmt.Errorf("absolute sandbox working directory is required")
	}
	execTarget, ok := provider.(interface{ GetDockerExecTarget() (string, bool) })
	if !ok {
		return nil, fmt.Errorf("sandbox does not expose a Docker container")
	}
	containerID, ok := execTarget.GetDockerExecTarget()
	if !ok || containerID == "" || threadID == "" {
		return nil, fmt.Errorf("Docker container and thread ID are required")
	}
	pathResolver, ok := provider.(agentmodel.ContainerPathResolver)
	if !ok {
		return nil, fmt.Errorf("Docker sandbox must resolve container paths")
	}
	dockerFilesystem := &DockerFilesystem{releaseContainer: releaseContainer, sandbox: provider, dir: path.Clean(workDir), root: path.Clean(workDir), containerID: containerID, pathResolver: pathResolver}
	dockerFilesystem.commands = NewDockerCommands(threadID, dockerFilesystem, containerID)
	return dockerFilesystem, nil
}

func (dockerFilesystem *DockerFilesystem) resolve(ctx context.Context, filePath string) (string, error) {
	err := ctx.Err()
	if err != nil {
		return "", normalizeDockerFileError(ctx, err)
	}
	if strings.ContainsRune(filePath, 0) {
		return "", agentmodel.ErrInvalidPath
	}
	dockerFilesystem.mu.RLock()
	workDir := dockerFilesystem.dir
	dockerFilesystem.mu.RUnlock()
	if filePath == "" {
		filePath = "."
	}
	resolvedPath := path.Clean(filePath)
	if !path.IsAbs(resolvedPath) {
		resolvedPath = path.Join(workDir, resolvedPath)
	}
	if resolvedPath != dockerFilesystem.root && dockerFilesystem.root != "/" && !strings.HasPrefix(resolvedPath, dockerFilesystem.root+"/") {
		return "", agentmodel.ErrInvalidPath
	}
	dockerFilesystem.mu.RLock()
	canonicalRoot := dockerFilesystem.canonicalRoot
	dockerFilesystem.mu.RUnlock()
	if canonicalRoot == "" {
		var err error
		canonicalRoot, err = dockerFilesystem.pathResolver.ResolveContainerPath(ctx, dockerFilesystem.root)
		if err != nil {
			return "", err
		}
		if !path.IsAbs(canonicalRoot) || path.Clean(canonicalRoot) != canonicalRoot {
			return "", agentmodel.ErrInvalidPath
		}
		dockerFilesystem.mu.Lock()
		if dockerFilesystem.canonicalRoot == "" {
			dockerFilesystem.canonicalRoot = canonicalRoot
		} else {
			canonicalRoot = dockerFilesystem.canonicalRoot
		}
		dockerFilesystem.mu.Unlock()
	}
	canonicalPath, err := dockerFilesystem.pathResolver.ResolveContainerPath(ctx, resolvedPath)
	if err != nil {
		return "", err
	}
	if !path.IsAbs(canonicalPath) || path.Clean(canonicalPath) != canonicalPath {
		return "", agentmodel.ErrInvalidPath
	}
	if canonicalPath != canonicalRoot && canonicalRoot != "/" && !strings.HasPrefix(canonicalPath, canonicalRoot+"/") {
		return "", agentmodel.ErrInvalidPath
	}
	return resolvedPath, nil
}

func (dockerFilesystem *DockerFilesystem) GetRoot() string { return dockerFilesystem.root }
func (dockerFilesystem *DockerFilesystem) Resolve(ctx context.Context, filePath string, _ bool) (string, error) {
	return dockerFilesystem.resolve(ctx, filePath)
}
func (dockerFilesystem *DockerFilesystem) Execute(ctx context.Context, request agentmodel.CommandRequest) (*agentmodel.CommandResult, error) {
	return dockerFilesystem.commands.Execute(ctx, request)
}
func (dockerFilesystem *DockerFilesystem) Start(ctx context.Context, request agentmodel.CommandRequest) (string, error) {
	return dockerFilesystem.commands.Start(ctx, request)
}
func (dockerFilesystem *DockerFilesystem) Wait(ctx context.Context, jobID, pattern string, offset int) (*agentmodel.CommandSnapshot, error) {
	return dockerFilesystem.commands.Wait(ctx, jobID, pattern, offset)
}
func (dockerFilesystem *DockerFilesystem) Cancel(ctx context.Context, jobID string) error {
	return dockerFilesystem.commands.Cancel(ctx, jobID)
}
func (dockerFilesystem *DockerFilesystem) Close(ctx context.Context) error {
	err := dockerFilesystem.commands.Close(ctx)
	if err != nil {
		return err
	}
	if dockerFilesystem.releaseContainer != nil {
		dockerFilesystem.releaseOnce.Do(dockerFilesystem.releaseContainer)
	}
	return nil
}

func buildDockerFileInfos(paths []string) []agentmodel.FileInfo {
	fileInfos := make([]agentmodel.FileInfo, 0, len(paths))
	for _, filePath := range paths {
		fileInfos = append(fileInfos, agentmodel.FileInfo{Path: filePath, IsDir: strings.HasSuffix(filePath, "/")})
	}
	return fileInfos
}
func (dockerFilesystem *DockerFilesystem) List(ctx context.Context, directoryPath string) ([]agentmodel.FileInfo, error) {
	directoryPath, err := dockerFilesystem.resolve(ctx, directoryPath)
	if err != nil {
		return nil, normalizeDockerFileError(ctx, err)
	}
	fileInfoProvider, ok := dockerFilesystem.sandbox.(agentmodel.FileInfoProvider)
	if ok {
		entries, err := fileInfoProvider.ListDirInfo(ctx, directoryPath, 1)
		if err != nil {
			return nil, normalizeDockerFileError(ctx, err)
		}
		fileInfos := make([]agentmodel.FileInfo, 0, len(entries))
		for _, entry := range entries {
			fileInfos = append(fileInfos, agentmodel.FileInfo{Path: entry.Path, IsDir: entry.IsDir, IsSymlink: entry.IsSymlink, Size: entry.Size})
		}
		return fileInfos, nil
	}
	entries, err := dockerFilesystem.sandbox.ListDir(ctx, directoryPath, 1)
	if err != nil {
		return nil, normalizeDockerFileError(ctx, err)
	}
	return buildDockerFileInfos(entries), nil
}
func (dockerFilesystem *DockerFilesystem) readContent(ctx context.Context, filePath string) (string, string, error) {
	resolvedPath, err := dockerFilesystem.resolve(ctx, filePath)
	if err != nil {
		return "", "", normalizeDockerFileError(ctx, err)
	}
	content, err := dockerFilesystem.sandbox.ReadFile(ctx, resolvedPath)
	if err != nil {
		return "", "", normalizeDockerFileError(ctx, err)
	}
	if len(content) > agentmodel.MaxFileSizeMB<<20 {
		return "", "", fmt.Errorf("file exceeds %d MiB", agentmodel.MaxFileSizeMB)
	}
	return resolvedPath, content, nil
}
func (dockerFilesystem *DockerFilesystem) Read(ctx context.Context, filePath string, offset, limit *int) (string, error) {
	_, content, err := dockerFilesystem.readContent(ctx, filePath)
	if err != nil {
		return "", err
	}
	return ReadFileLines(content, offset, limit), nil
}
func (dockerFilesystem *DockerFilesystem) Write(ctx context.Context, filePath, content string) (*agentmodel.WriteResult, error) {
	resolvedPath, err := dockerFilesystem.resolve(ctx, filePath)
	if err != nil {
		return nil, normalizeDockerFileError(ctx, err)
	}
	err = dockerFilesystem.sandbox.WriteFile(ctx, resolvedPath, content, false)
	if err != nil {
		return nil, normalizeDockerFileError(ctx, err)
	}
	return &agentmodel.WriteResult{Path: filePath}, nil
}
func (dockerFilesystem *DockerFilesystem) Edit(ctx context.Context, filePath, oldText, newText string, replaceAll bool) (*agentmodel.EditResult, error) {
	if oldText == "" {
		return nil, fmt.Errorf("old text is required")
	}
	resolvedPath, content, err := dockerFilesystem.readContent(ctx, filePath)
	if err != nil {
		return nil, err
	}
	// Providers can complete a read after cancellation. Do not begin a second
	// remote operation once the caller has stopped this edit.
	contextErr := ctx.Err()
	if contextErr != nil {
		return nil, contextErr
	}
	updatedContent, occurrences, err := ReplaceFileText(content, oldText, newText, replaceAll)
	if err != nil {
		return &agentmodel.EditResult{Path: filePath, Occurrences: occurrences}, err
	}
	contextErr = ctx.Err()
	if contextErr != nil {
		return nil, contextErr
	}
	err = dockerFilesystem.sandbox.WriteFile(ctx, resolvedPath, updatedContent, false)
	if err != nil {
		return nil, normalizeDockerFileError(ctx, err)
	}
	return &agentmodel.EditResult{Path: filePath, Occurrences: occurrences}, nil
}
func (dockerFilesystem *DockerFilesystem) Grep(ctx context.Context, pattern, directoryPath, glob string) ([]agentmodel.GrepMatch, error) {
	directoryPath, err := dockerFilesystem.resolve(ctx, directoryPath)
	if err != nil {
		return nil, normalizeDockerFileError(ctx, err)
	}
	matches, _, err := dockerFilesystem.sandbox.Grep(ctx, directoryPath, pattern, agentmodel.SandboxGrepOptions{Glob: glob, CaseSensitive: true, MaxResults: 100})
	if err != nil {
		return nil, normalizeDockerFileError(ctx, err)
	}
	grepMatches := make([]agentmodel.GrepMatch, 0, len(matches))
	for _, match := range matches {
		grepMatches = append(grepMatches, agentmodel.GrepMatch{Path: match.Path, Line: match.LineNumber, Text: match.Line})
	}
	return grepMatches, nil
}
func (dockerFilesystem *DockerFilesystem) Glob(ctx context.Context, pattern, directoryPath string) ([]agentmodel.FileInfo, error) {
	directoryPath, err := dockerFilesystem.resolve(ctx, directoryPath)
	if err != nil {
		return nil, normalizeDockerFileError(ctx, err)
	}
	entries, _, err := dockerFilesystem.sandbox.Glob(ctx, directoryPath, pattern, agentmodel.SandboxGlobOptions{MaxResults: globMaxResults})
	if err != nil {
		return nil, normalizeDockerFileError(ctx, err)
	}
	return buildDockerFileInfos(entries), nil
}
func (dockerFilesystem *DockerFilesystem) UploadFiles(ctx context.Context, files []struct {
	Path    string
	Content []byte
}) ([]agentmodel.FileUploadResponse, error) {
	uploadResponses := make([]agentmodel.FileUploadResponse, 0, len(files))
	for _, file := range files {
		resolvedPath, err := dockerFilesystem.resolve(ctx, file.Path)
		if err != nil {
			return uploadResponses, normalizeDockerFileError(ctx, err)
		}
		err = dockerFilesystem.sandbox.UpdateFile(ctx, resolvedPath, file.Content)
		if err != nil {
			return uploadResponses, normalizeDockerFileError(ctx, err)
		}
		uploadResponses = append(uploadResponses, agentmodel.FileUploadResponse{Path: file.Path})
	}
	return uploadResponses, nil
}
func (dockerFilesystem *DockerFilesystem) ChangeDir(ctx context.Context, directoryPath string) error {
	directoryPath, err := dockerFilesystem.resolve(ctx, directoryPath)
	if err != nil {
		return normalizeDockerFileError(ctx, err)
	}
	_, err = dockerFilesystem.sandbox.ListDir(ctx, directoryPath, 1)
	if err != nil {
		return normalizeDockerFileError(ctx, err)
	}
	dockerFilesystem.mu.Lock()
	dockerFilesystem.dir = directoryPath
	dockerFilesystem.mu.Unlock()
	return nil
}

func (dockerFilesystem *DockerFilesystem) Delete(ctx context.Context, filePath string) (string, error) {
	resolvedPath, err := dockerFilesystem.resolve(ctx, filePath)
	if err != nil {
		return "", normalizeDockerFileError(ctx, err)
	}
	if resolvedPath == dockerFilesystem.root {
		return "", agentmodel.ErrInvalidPath
	}
	dockerCommand := exec.CommandContext(ctx, "docker", "exec", dockerFilesystem.containerID, "rm", "-f", "--", resolvedPath)
	output, err := dockerCommand.CombinedOutput()
	if err != nil {
		deleteErr := fmt.Errorf("docker delete %s: %s: %w", filePath, strings.TrimSpace(string(output)), err)
		return "", normalizeDockerFileError(ctx, deleteErr)
	}
	return "Deleted file " + filePath, nil
}

func (dockerFilesystem *DockerFilesystem) ApplyPatch(ctx context.Context, patch string) (string, error) {
	return ApplyWorkspacePatch(ctx, dockerFilesystem, patch)
}

var _ agentmodel.Filesystem = (*DockerFilesystem)(nil)
var _ agentmodel.CommandService = (*DockerFilesystem)(nil)
var _ agentmodel.ToolFilesystem = (*DockerFilesystem)(nil)
var _ patchFilesystem = (*DockerFilesystem)(nil)

const (
	dockerPatchNotFoundExitCode      = 44
	dockerPatchAlreadyExistsExitCode = 73
)

func (dockerFilesystem *DockerFilesystem) HasFile(ctx context.Context, filePath string) (bool, error) {
	resolvedPath, err := dockerFilesystem.resolve(ctx, filePath)
	if err != nil {
		return false, normalizeDockerFileError(ctx, err)
	}
	existenceProvider, ok := dockerFilesystem.sandbox.(interface {
		FileExists(context.Context, string) (bool, error)
	})
	if ok {
		exists, providerErr := existenceProvider.FileExists(ctx, resolvedPath)
		if providerErr != nil {
			return false, normalizeDockerFileError(ctx, providerErr)
		}
		return exists, nil
	}
	dockerCommand := exec.CommandContext(ctx, "docker", "exec", dockerFilesystem.containerID, "python3", "-c", dockerPatchScript, "stat", dockerFilesystem.root, resolvedPath)
	output, err := dockerCommand.CombinedOutput()
	if err == nil {
		return true, nil
	}
	contextErr := ctx.Err()
	if contextErr != nil {
		return false, normalizeDockerFileError(ctx, fmt.Errorf("docker stat %s: %s: %w", filePath, strings.TrimSpace(string(output)), err))
	}
	exitErr := &exec.ExitError{}
	isExitError := errors.As(err, &exitErr)
	if isExitError {
		exitCode := exitErr.ExitCode()
		if exitCode == dockerPatchNotFoundExitCode {
			return false, nil
		}
	}
	existenceErr := fmt.Errorf("docker stat %s: %s: %w", filePath, strings.TrimSpace(string(output)), err)
	return false, normalizeDockerFileError(ctx, existenceErr)
}

func (dockerFilesystem *DockerFilesystem) CreateFileNoReplace(ctx context.Context, filePath, content string) (*agentmodel.WriteResult, error) {
	resolvedPath, err := dockerFilesystem.resolve(ctx, filePath)
	if err != nil {
		return nil, normalizeDockerFileError(ctx, err)
	}
	creationProvider, ok := dockerFilesystem.sandbox.(interface {
		CreateFileNoReplace(context.Context, string, string) error
	})
	if ok {
		err = creationProvider.CreateFileNoReplace(ctx, resolvedPath, content)
		if err != nil {
			return nil, normalizeDockerFileError(ctx, err)
		}
		contextErr := ctx.Err()
		if contextErr != nil {
			return nil, contextErr
		}
		return &agentmodel.WriteResult{Path: filePath}, nil
	}
	dockerCommand := exec.CommandContext(ctx, "docker", "exec", "-i", dockerFilesystem.containerID, "python3", "-c", dockerPatchScript, "create", dockerFilesystem.root, resolvedPath)
	dockerCommand.Stdin = strings.NewReader(content)
	output, err := dockerCommand.CombinedOutput()
	if err == nil {
		contextErr := ctx.Err()
		if contextErr != nil {
			return nil, contextErr
		}
		return &agentmodel.WriteResult{Path: filePath}, nil
	}
	contextErr := ctx.Err()
	if contextErr != nil {
		return nil, normalizeDockerFileError(ctx, fmt.Errorf("docker create %s: %s: %w", filePath, strings.TrimSpace(string(output)), err))
	}
	exitErr := &exec.ExitError{}
	isExitError := errors.As(err, &exitErr)
	if isExitError {
		exitCode := exitErr.ExitCode()
		if exitCode == dockerPatchAlreadyExistsExitCode {
			return nil, fmt.Errorf("%w: %s", agentmodel.ErrAlreadyExists, filePath)
		}
	}
	createErr := fmt.Errorf("docker create %s: %s: %w", filePath, strings.TrimSpace(string(output)), err)
	return nil, normalizeDockerFileError(ctx, createErr)
}

// Older providers wrap transport failures as text. Retain their details while
// preserving cancellation identity for the Graph's system-error boundary.
func normalizeDockerFileError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), err)
	}
	return err
}

// The AIO image runs Python. Keep all path traversal and no-replace installation
// inside one process, anchored to directory descriptors in the container.
const dockerPatchScript = `import os, secrets, shutil, sys
operation, root, target = sys.argv[1:]
relative = os.path.relpath(target, root)
if relative == ".." or relative.startswith("../"):
    raise ValueError("path escapes filesystem root")
parts = relative.split("/")
flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW
fd = os.open(root, flags)
temporary = None
try:
    for part in parts[:-1]:
        if part in ("", "."):
            continue
        if operation == "create":
            try:
                os.mkdir(part, dir_fd=fd)
            except FileExistsError:
                pass
        child = os.open(part, flags, dir_fd=fd)
        os.close(fd)
        fd = child
    if operation == "stat":
        os.stat(parts[-1], dir_fd=fd, follow_symlinks=False)
    else:
        candidate = ".patch-" + secrets.token_hex(16)
        file = os.open(candidate, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o644, dir_fd=fd)
        temporary = candidate
        with os.fdopen(file, "wb") as output:
            shutil.copyfileobj(sys.stdin.buffer, output)
        os.link(temporary, parts[-1], src_dir_fd=fd, dst_dir_fd=fd, follow_symlinks=False)
except FileNotFoundError:
    if operation == "stat":
        sys.exit(44)
    raise
except FileExistsError:
    sys.exit(73)
finally:
    if temporary is not None:
        os.unlink(temporary, dir_fd=fd)
    os.close(fd)
`
