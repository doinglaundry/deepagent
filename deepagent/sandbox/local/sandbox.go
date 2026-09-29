// Package local implements Sandbox on the host fs with per-session path mappings.
package local

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"eino-cli/deepagent/sandbox"
	"eino-cli/deepagent/sandbox/paths"
	"eino-cli/deepagent/sandbox/search"
)

const (
	commandTimeout   = 10 * time.Minute
	defaultListDepth = 2
)

const (
	fileOperationRead   = "read"
	fileOperationWrite  = "write"
	fileOperationUpdate = "update"
	fileOperationList   = "list"
	fileOperationGlob   = "glob"
	fileOperationGrep   = "grep"
)

type Sandbox struct {
	sandboxID string
	sessionID string
	mounts    []sandboxpaths.MountMapping
	mountInfo map[string]os.FileInfo
}

func newSandbox(sessionID, sandboxID string, mounts []sandboxpaths.MountMapping) *Sandbox {
	identities := make(map[string]os.FileInfo, len(mounts))
	for _, mount := range mounts {
		info, err := os.Lstat(mount.HostPath)
		if err == nil && info.IsDir() {
			identities[mount.HostPath] = info
		}
	}
	return &Sandbox{
		mountInfo: identities,
		sandboxID: sandboxID,
		sessionID: sessionID,
		mounts:    append([]sandboxpaths.MountMapping(nil), mounts...),
	}
}

func (s *Sandbox) ID() string { return s.sandboxID }

func (s *Sandbox) SessionID() string { return s.sessionID }

func (s *Sandbox) ExecuteCommand(ctx context.Context, command string) (string, error) {
	hostPathCommand := replaceVirtualPathsWithHostPaths(s.mounts, command, shellCommandText)
	shell, err := pickShell()
	if err != nil {
		return "", sandbox.NewRuntimeError(err.Error())
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	args := buildShellArgs(shell, hostPathCommand)
	shellProcess := exec.CommandContext(timeoutCtx, args[0], args[1:]...)
	stdout, stderr, exitCode, startErr := runShell(shellProcess)

	output := formatCommandOutput(stdout, stderr, exitCode)
	maskedOutput := s.maskHostPaths(output)
	if startErr != nil && exitCode == 0 {
		return maskedOutput, sandbox.NewCommandError(startErr.Error(), command, exitCode)
	}
	return maskedOutput, nil
}

func formatCommandOutput(stdout, stderr string, exitCode int) string {
	out := stdout
	if stderr != "" && out != "" {
		out += "\nStd Error:\n" + stderr
	} else if stderr != "" {
		out = stderr
	}
	if exitCode != 0 {
		out += fmt.Sprintf("\nExit Code: %d", exitCode)
	}
	if out == "" {
		return "(no output)"
	}
	return out
}

func pickShell() (string, error) {
	for _, candidateShell := range getShellCandidates() {
		shellPath, ok := getUsableShell(candidateShell)
		if ok {
			return shellPath, nil
		}
	}
	return "", errors.New("no usable shell found (tried zsh/bash/sh on unix or powershell/cmd on windows)")
}

func getShellCandidates() []string {
	if runtime.GOOS != "windows" {
		return []string{"/bin/zsh", "/bin/bash", "/bin/sh", "sh"}
	}
	return []string{"pwsh", "pwsh.exe", "powershell", "powershell.exe", "cmd.exe"}
}

func getUsableShell(candidateShell string) (string, bool) {
	if filepath.IsAbs(candidateShell) {
		info, err := os.Stat(candidateShell)
		return candidateShell, err == nil && !info.IsDir()
	}
	shellPath, err := exec.LookPath(candidateShell)
	return shellPath, err == nil
}

func buildShellArgs(shellPath, command string) []string {
	if runtime.GOOS != "windows" {
		return []string{shellPath, "-c", command}
	}
	shellName := strings.ToLower(filepath.Base(shellPath))
	switch {
	case strings.HasPrefix(shellName, "pwsh"), strings.HasPrefix(shellName, "powershell"):
		return []string{shellPath, "-NoProfile", "-Command", command}
	case strings.HasPrefix(shellName, "cmd"):
		return []string{shellPath, "/c", command}
	default:
		return []string{shellPath, "-c", command}
	}
}

// runShell runs process and splits stdout/stderr/exitCode; startErr is non-nil only on start failure.
func runShell(process *exec.Cmd) (stdout, stderr string, exitCode int, startErr error) {
	var so, se strings.Builder
	process.Stdout = &so
	process.Stderr = &se
	err := process.Run()
	stdout = so.String()
	stderr = se.String()
	if err == nil {
		return stdout, stderr, 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout, stderr, exitErr.ExitCode(), nil
	}
	return stdout, stderr, 0, err
}

// ReadFile reads text from a virtual path and masks host paths in the returned content.
func (s *Sandbox) ReadFile(ctx context.Context, virtualPath string) (string, error) {
	resolved, err := resolveSandboxPath(s.mounts, virtualPath)
	if err != nil {
		return "", err
	}
	var content []byte
	if resolved.Mapped {
		root, rootErr := s.openMount(resolved.Mount)
		if rootErr != nil {
			return "", wrapFileError(rootErr, virtualPath, fileOperationRead)
		}
		defer root.Close()
		content, err = root.ReadFile(resolved.RelativePath)
	} else {
		content, err = os.ReadFile(resolved.HostPath)
	}
	if err != nil {
		return "", wrapFileError(err, virtualPath, fileOperationRead)
	}
	return s.maskHostPaths(string(content)), nil
}

// WriteFile writes text to a virtual path after translating virtual paths in content.
func (s *Sandbox) WriteFile(ctx context.Context, virtualPath, content string, appendMode bool) error {
	resolved, err := s.prepareWritableHostPath(virtualPath, fileOperationWrite)
	if err != nil {
		return err
	}
	contentWithHostPaths := replaceVirtualPathsWithHostPaths(s.mounts, content, fileContentText)
	if resolved.Mapped {
		root, rootErr := s.openMount(resolved.Mount)
		if rootErr != nil {
			return wrapFileError(rootErr, virtualPath, fileOperationWrite)
		}
		defer root.Close()
		err = writeFileRoot(root, resolved.RelativePath, []byte(contentWithHostPaths), appendMode)
	} else {
		err = writeTextFile(resolved.HostPath, contentWithHostPaths, appendMode)
	}
	if err != nil {
		return wrapFileError(err, virtualPath, fileOperationWrite)
	}

	return nil
}

// UpdateFile overwrites binary content without text path translation.
func (s *Sandbox) UpdateFile(ctx context.Context, virtualPath string, content []byte) error {
	resolved, err := s.prepareWritableHostPath(virtualPath, fileOperationUpdate)
	if err != nil {
		return err
	}
	if resolved.Mapped {
		root, rootErr := s.openMount(resolved.Mount)
		if rootErr != nil {
			return wrapFileError(rootErr, virtualPath, fileOperationUpdate)
		}
		defer root.Close()
		err = writeFileRoot(root, resolved.RelativePath, content, false)
	} else {
		err = os.WriteFile(resolved.HostPath, content, 0o644)
	}
	if err != nil {
		return wrapFileError(err, virtualPath, fileOperationUpdate)
	}
	return nil
}

func (s *Sandbox) prepareWritableHostPath(virtualPath, operation string) (sandboxpaths.ResolvedPath, error) {
	resolved, err := resolveSandboxPath(s.mounts, virtualPath)
	if err != nil {
		return sandboxpaths.ResolvedPath{}, err
	}
	target := canonicalPath(resolved.HostPath)
	if resolved.Mapped {
		relative, relativeErr := filepath.Rel(canonicalPath(resolved.Mount.HostPath), target)
		if relativeErr != nil || !filepath.IsLocal(relative) {
			return sandboxpaths.ResolvedPath{}, sandbox.NewPermissionError("path escapes mount root", virtualPath)
		}
		resolved.RelativePath = relative
	}
	if isReadOnlyPath(s.mounts, target) {
		return sandboxpaths.ResolvedPath{}, sandbox.NewPermissionError("read-only file system", virtualPath)
	}
	if !resolved.Mapped {
		err = os.MkdirAll(filepath.Dir(resolved.HostPath), 0o755)
		if err != nil {
			return sandboxpaths.ResolvedPath{}, wrapFileError(err, virtualPath, operation)
		}
	}
	return resolved, nil
}

// ListDir returns virtual entries under a virtual path up to maxDepth.
func (s *Sandbox) ListDir(ctx context.Context, virtualPath string, maxDepth int) ([]string, error) {
	if maxDepth <= 0 {
		maxDepth = defaultListDepth
	}
	resolved, err := resolveSandboxPath(s.mounts, virtualPath)
	if err != nil {
		return nil, err
	}
	var hostEntries []string
	if resolved.Mapped {
		root, rootErr := s.openMount(resolved.Mount)
		if rootErr != nil {
			return nil, wrapFileError(rootErr, virtualPath, fileOperationList)
		}
		defer root.Close()
		hostEntries, err = listDirRoot(root, resolved.RelativePath, maxDepth)
	} else {
		hostEntries, err = listDir(resolved.HostPath, maxDepth)
	}
	if err != nil {
		return nil, wrapFileError(err, virtualPath, fileOperationList)
	}
	return s.reverseListEntries(hostEntries), nil
}

func (s *Sandbox) reverseListEntries(hostEntries []string) []string {
	virtualEntries := make([]string, len(hostEntries))
	for i, hostEntry := range hostEntries {
		virtualEntries[i] = s.reverseListEntry(hostEntry)
	}
	return virtualEntries
}

func (s *Sandbox) reverseListEntry(hostEntry string) string {
	isDir := strings.HasSuffix(hostEntry, "/") || strings.HasSuffix(hostEntry, `\`)
	virtualEntry := sandbox.ReverseResolvePath(s.mounts, strings.TrimRight(hostEntry, `/\`))
	if isDir && !strings.HasSuffix(virtualEntry, "/") {
		return virtualEntry + "/"
	}
	return virtualEntry
}

// Glob returns virtual paths matching pattern under virtualPath.
func (s *Sandbox) Glob(ctx context.Context, virtualPath, pattern string, opts sandbox.GlobOpts) ([]string, bool, error) {
	resolved, err := resolveSandboxPath(s.mounts, virtualPath)
	if err != nil {
		return nil, false, err
	}
	var source fs.FS
	if resolved.Mapped {
		root, openErr := s.openMount(resolved.Mount)
		if openErr != nil {
			return nil, false, openErr
		}
		defer root.Close()
		source, err = fs.Sub(root.FS(), filepath.ToSlash(resolved.RelativePath))
		if err != nil {
			return nil, false, err
		}
	} else {
		root, openErr := os.OpenRoot(resolved.HostPath)
		if openErr != nil {
			return nil, false, openErr
		}
		defer root.Close()
		source = root.FS()
	}
	hostMatches, truncated, err := search.FindGlobMatchesFS(source, resolved.HostPath, pattern, search.GlobOpts{
		IncludeDirs: opts.IncludeDirs,
		MaxResults:  opts.MaxResults,
	})
	if err != nil {
		return nil, false, wrapFileError(err, virtualPath, fileOperationGlob)
	}
	return s.reverseHostPaths(hostMatches), truncated, nil
}

// Grep returns virtual path matches for pattern under virtualPath.
func (s *Sandbox) Grep(ctx context.Context, virtualPath, pattern string, opts sandbox.GrepOpts) ([]sandbox.GrepMatch, bool, error) {
	resolved, err := resolveSandboxPath(s.mounts, virtualPath)
	if err != nil {
		return nil, false, err
	}
	var source fs.FS
	if resolved.Mapped {
		root, openErr := s.openMount(resolved.Mount)
		if openErr != nil {
			return nil, false, openErr
		}
		defer root.Close()
		source, err = fs.Sub(root.FS(), filepath.ToSlash(resolved.RelativePath))
		if err != nil {
			return nil, false, err
		}
	} else {
		root, openErr := os.OpenRoot(resolved.HostPath)
		if openErr != nil {
			return nil, false, openErr
		}
		defer root.Close()
		source = root.FS()
	}
	hostMatches, truncated, err := search.FindGrepMatchesFS(source, resolved.HostPath, pattern, search.GrepOpts{
		Glob:          opts.Glob,
		Literal:       opts.Literal,
		CaseSensitive: opts.CaseSensitive,
		MaxResults:    opts.MaxResults,
	})
	if err != nil {
		return nil, false, wrapFileError(err, virtualPath, fileOperationGrep)
	}
	return s.reverseGrepMatches(hostMatches), truncated, nil
}

func writeTextFile(hostPath, content string, appendMode bool) error {
	file, err := os.OpenFile(hostPath, buildWriteFileFlag(appendMode), 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(content)
	return err
}

// The policy-selected canonical path must not follow newly substituted symlinks.
func writeFileRoot(root *os.Root, relative string, content []byte, appendMode bool) error {
	parent, err := root.OpenRoot(".")
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	for _, component := range strings.Split(filepath.Dir(relative), string(filepath.Separator)) {
		if component == "." {
			continue
		}
		err = parent.Mkdir(component, 0755)
		if err != nil && !os.IsExist(err) {
			return err
		}
		expected, statErr := parent.Lstat(component)
		if statErr != nil {
			return statErr
		}
		if !expected.IsDir() {
			return fmt.Errorf("write parent is not a directory: %s", component)
		}
		next, openErr := parent.OpenRoot(component)
		if openErr != nil {
			return openErr
		}
		actual, statErr := next.Stat(".")
		if statErr != nil || !os.SameFile(expected, actual) {
			_ = next.Close()
			return fmt.Errorf("write parent changed: %s", component)
		}
		_ = parent.Close()
		parent = next
	}
	name := filepath.Base(relative)
	expected, err := parent.Lstat(name)
	missing := os.IsNotExist(err)
	if err != nil && !missing {
		return err
	}
	if !missing && !expected.Mode().IsRegular() {
		return fmt.Errorf("write target is not a regular file: %s", name)
	}
	flags := os.O_WRONLY
	if missing {
		flags |= os.O_CREATE | os.O_EXCL
	}
	if appendMode {
		flags |= os.O_APPEND
	}
	file, err := parent.OpenFile(name, flags, 0644)
	if err != nil {
		return err
	}
	defer file.Close()
	if !missing {
		actual, statErr := file.Stat()
		if statErr != nil || !os.SameFile(expected, actual) {
			return fmt.Errorf("write target changed: %s", name)
		}
		if !appendMode {
			err = file.Truncate(0)
			if err != nil {
				return err
			}
		}
	}
	_, err = file.Write(content)
	return err
}

func buildWriteFileFlag(appendMode bool) int {
	flag := os.O_CREATE | os.O_WRONLY
	if appendMode {
		return flag | os.O_APPEND
	}
	return flag | os.O_TRUNC
}

func (s *Sandbox) reverseHostPaths(hostPaths []string) []string {
	virtualPaths := make([]string, len(hostPaths))
	for i, hostPath := range hostPaths {
		virtualPaths[i] = sandbox.ReverseResolvePath(s.mounts, hostPath)
	}
	return virtualPaths
}

func (s *Sandbox) reverseGrepMatches(hostMatches []search.GrepMatch) []sandbox.GrepMatch {
	virtualMatches := make([]sandbox.GrepMatch, len(hostMatches))
	for i, hostMatch := range hostMatches {
		virtualMatches[i] = sandbox.GrepMatch{
			Path:       sandbox.ReverseResolvePath(s.mounts, hostMatch.Path),
			LineNumber: hostMatch.LineNumber,
			Line:       s.maskHostPaths(hostMatch.Line),
		}
	}
	return virtualMatches
}

func (s *Sandbox) maskHostPaths(content string) string {
	return sandbox.MaskHostPathsInOutput(s.mounts, content)
}

// wrapFileError maps OS errors to FileNotFoundError / PermissionError / FileError.
func wrapFileError(err error, virtualPath, operation string) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return sandbox.NewFileNotFoundError(virtualPath)
	case errors.Is(err, fs.ErrPermission):
		return sandbox.NewPermissionError(err.Error(), virtualPath)
	}
	return sandbox.NewFileError(err.Error(), virtualPath, operation)
}

// Reopening a mount must still address the directory selected at construction.
func (s *Sandbox) openMount(mount sandboxpaths.MountMapping) (*os.Root, error) {
	expected := s.mountInfo[mount.HostPath]
	if expected == nil {
		return nil, fmt.Errorf("mount is not a directory: %s", mount.VirtualPath)
	}
	info, err := os.Lstat(mount.HostPath)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(expected, info) || !info.IsDir() {
		return nil, fmt.Errorf("mount was replaced: %s", mount.VirtualPath)
	}
	root, err := os.OpenRoot(mount.HostPath)
	if err != nil {
		return nil, err
	}
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(expected, opened) {
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("mount changed while opening: %s", mount.VirtualPath)
	}
	return root, nil
}
