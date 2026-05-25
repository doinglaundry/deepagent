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
	"sync"
	"time"

	"eino-cli/backend/sandbox"
	"eino-cli/backend/sandbox/search"
	"eino-cli/backend/sandboxpaths"
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
	writtenMu sync.RWMutex
	written   map[string]any
}

func newSandbox(sessionID, sandboxID string, mounts []sandboxpaths.MountMapping) *Sandbox {
	return &Sandbox{
		sandboxID: sandboxID,
		sessionID: sessionID,
		mounts:    append([]sandboxpaths.MountMapping(nil), mounts...),
		written:   map[string]any{},
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
	resolvedPath, err := resolvePath(s.mounts, virtualPath)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(resolvedPath.HostPath)
	if err != nil {
		return "", wrapFileError(err, virtualPath, fileOperationRead)
	}
	return s.maskHostPaths(string(content)), nil
}

// WriteFile writes text to a virtual path after translating virtual paths in content.
func (s *Sandbox) WriteFile(ctx context.Context, virtualPath, content string, appendMode bool) error {
	resolvedPath, err := s.prepareWritableHostPath(virtualPath, fileOperationWrite)
	if err != nil {
		return err
	}
	contentWithHostPaths := replaceVirtualPathsWithHostPaths(s.mounts, content, fileContentText)
	if err := writeTextFile(resolvedPath.HostPath, contentWithHostPaths, appendMode); err != nil {
		return wrapFileError(err, virtualPath, fileOperationWrite)
	}

	s.recordWrittenHostPath(resolvedPath.HostPath)
	return nil
}

// UpdateFile overwrites binary content without text path translation or masking bookkeeping.
func (s *Sandbox) UpdateFile(ctx context.Context, virtualPath string, content []byte) error {
	resolvedPath, err := s.prepareWritableHostPath(virtualPath, fileOperationUpdate)
	if err != nil {
		return err
	}
	if err := os.WriteFile(resolvedPath.HostPath, content, 0o644); err != nil {
		return wrapFileError(err, virtualPath, fileOperationUpdate)
	}
	return nil
}

func (s *Sandbox) prepareWritableHostPath(virtualPath, operation string) (resolvedHostPath, error) {
	resolvedPath, err := resolvePath(s.mounts, virtualPath)
	if err != nil {
		return resolvedHostPath{}, err
	}
	if resolvedPath.Mapping != nil && resolvedPath.Mapping.ReadOnly {
		return resolvedHostPath{}, sandbox.NewPermissionError("read-only file system", virtualPath)
	}
	if isReadOnlyPath(s.mounts, resolvedPath.HostPath) {
		return resolvedHostPath{}, sandbox.NewPermissionError("read-only file system", virtualPath)
	}
	if err := os.MkdirAll(filepath.Dir(resolvedPath.HostPath), 0o755); err != nil {
		return resolvedHostPath{}, wrapFileError(err, virtualPath, operation)
	}
	return resolvedPath, nil
}

// ListDir returns virtual entries under a virtual path up to maxDepth.
func (s *Sandbox) ListDir(ctx context.Context, virtualPath string, maxDepth int) ([]string, error) {
	if maxDepth <= 0 {
		maxDepth = defaultListDepth
	}
	resolvedPath, err := resolvePath(s.mounts, virtualPath)
	if err != nil {
		return nil, err
	}
	hostEntries, err := listDir(resolvedPath.HostPath, maxDepth)
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
	resolvedPath, err := resolvePath(s.mounts, virtualPath)
	if err != nil {
		return nil, false, err
	}
	hostMatches, truncated, err := search.FindGlobMatches(resolvedPath.HostPath, pattern, search.GlobOpts{
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
	resolvedPath, err := resolvePath(s.mounts, virtualPath)
	if err != nil {
		return nil, false, err
	}
	hostMatches, truncated, err := search.FindGrepMatches(resolvedPath.HostPath, pattern, search.GrepOpts{
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

func buildWriteFileFlag(appendMode bool) int {
	flag := os.O_CREATE | os.O_WRONLY
	if appendMode {
		return flag | os.O_APPEND
	}
	return flag | os.O_TRUNC
}

func (s *Sandbox) recordWrittenHostPath(hostPath string) {
	s.writtenMu.Lock()
	s.written[hostPath] = struct{}{}
	s.writtenMu.Unlock()
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
