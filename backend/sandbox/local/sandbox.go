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

const commandTimeout = 10 * time.Minute

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

func (s *Sandbox) ExecuteCommand(ctx context.Context, cmd string) (string, error) {
	resolved := resolvePathsInCommand(s.mounts, cmd)
	shell, err := pickShell()
	if err != nil {
		return "", sandbox.NewRuntimeError(err.Error())
	}

	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	args, env := shellArgs(shell, resolved)
	c := exec.CommandContext(ctx, args[0], args[1:]...)
	if env != nil {
		c.Env = env
	}
	stdout, stderr, exitCode, runErr := runShell(c)

	out := sandbox.MaskHostPathsInOutput(s.mounts, formatCommandOutput(stdout, stderr, exitCode))
	if runErr != nil && exitCode == 0 {
		return out, sandbox.NewCommandError(runErr.Error(), cmd, exitCode)
	}
	return out, nil
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

// pickShell returns the first usable shell: zsh > bash > sh, or pwsh > cmd on Windows.
func pickShell() (string, error) {
	var candidates []string
	if runtime.GOOS == "windows" {
		candidates = []string{"pwsh", "pwsh.exe", "powershell", "powershell.exe", "cmd.exe"}
	} else {
		candidates = []string{"/bin/zsh", "/bin/bash", "/bin/sh", "sh"}
	}
	for _, sh := range candidates {
		if filepath.IsAbs(sh) {
			if info, err := os.Stat(sh); err == nil && !info.IsDir() {
				return sh, nil
			}
			continue
		}
		if p, err := exec.LookPath(sh); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no usable shell found (tried zsh/bash/sh on unix or powershell/cmd on windows)")
}

// shellArgs returns the argv for cmd based on shell flavour; env=nil inherits.
func shellArgs(shell, cmd string) (args []string, env []string) {
	if runtime.GOOS != "windows" {
		return []string{shell, "-c", cmd}, nil
	}
	name := strings.ToLower(filepath.Base(shell))
	switch {
	case strings.HasPrefix(name, "pwsh"), strings.HasPrefix(name, "powershell"):
		return []string{shell, "-NoProfile", "-Command", cmd}, nil
	case strings.HasPrefix(name, "cmd"):
		return []string{shell, "/c", cmd}, nil
	default:
		return []string{shell, "-c", cmd}, nil
	}
}

// runShell runs c and splits stdout/stderr/exitCode; runErr is non-nil only on start failure.
func runShell(c *exec.Cmd) (stdout, stderr string, exitCode int, runErr error) {
	var so, se strings.Builder
	c.Stdout = &so
	c.Stderr = &se
	err := c.Run()
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

// ReadFile reads the host file; agent-written files get path masking on the way out.
func (s *Sandbox) ReadFile(ctx context.Context, path string) (string, error) {
	r, err := resolvePath(s.mounts, path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(r.Path)
	if err != nil {
		return "", wrapFileError(err, path, "read")
	}
	return sandbox.MaskHostPathsInOutput(s.mounts, string(data)), nil
}

// WriteFile creates/overwrites/appends; read-only mounts surface as PermissionError.
func (s *Sandbox) WriteFile(ctx context.Context, path, content string, appendMode bool) error {
	r, err := s.prepareWritePath(path, "write")
	if err != nil {
		return err
	}
	flag := os.O_CREATE | os.O_WRONLY
	if appendMode {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(r.Path, flag, 0o644)
	if err != nil {
		return wrapFileError(err, path, "write")
	}
	defer f.Close()
	if _, err := f.WriteString(resolvePathsInContent(s.mounts, content)); err != nil {
		return wrapFileError(err, path, "write")
	}

	s.writtenMu.Lock()
	s.written[r.Path] = struct{}{}
	s.writtenMu.Unlock()
	return nil
}

// UpdateFile is the binary overwrite path; never registers agent-written (no strings to mask).
func (s *Sandbox) UpdateFile(ctx context.Context, path string, content []byte) error {
	r, err := s.prepareWritePath(path, "update")
	if err != nil {
		return err
	}
	if err := os.WriteFile(r.Path, content, 0o644); err != nil {
		return wrapFileError(err, path, "update")
	}
	return nil
}

func (s *Sandbox) prepareWritePath(path, op string) (resolved, error) {
	r, err := resolvePath(s.mounts, path)
	if err != nil {
		return resolved{}, err
	}
	if r.Mapping != nil && r.Mapping.ReadOnly || isReadOnlyPath(s.mounts, r.Path) {
		return resolved{}, sandbox.NewPermissionError("read-only file system", path)
	}
	if err := os.MkdirAll(filepath.Dir(r.Path), 0o755); err != nil {
		return resolved{}, wrapFileError(err, path, op)
	}
	return r, nil
}

// ListDir returns entries under path up to maxDepth (default 2).
func (s *Sandbox) ListDir(ctx context.Context, path string, maxDepth int) ([]string, error) {
	if maxDepth <= 0 {
		maxDepth = 2
	}
	r, err := resolvePath(s.mounts, path)
	if err != nil {
		return nil, err
	}
	entries, err := listDir(r.Path, maxDepth)
	if err != nil {
		return nil, wrapFileError(err, path, "list")
	}
	for i, e := range entries {
		entries[i] = s.reverseListEntry(e)
	}
	return entries, nil
}

func (s *Sandbox) reverseListEntry(entry string) string {
	isDir := strings.HasSuffix(entry, "/") || strings.HasSuffix(entry, `\`)
	reversed := sandbox.ReverseResolvePath(s.mounts, strings.TrimRight(entry, `/\`))
	if isDir && !strings.HasSuffix(reversed, "/") {
		return reversed + "/"
	}
	return reversed
}

// Glob walks path matching pattern via search.FindGlobMatches.
func (s *Sandbox) Glob(ctx context.Context, path, pattern string, opts sandbox.GlobOpts) ([]string, bool, error) {
	r, err := resolvePath(s.mounts, path)
	if err != nil {
		return nil, false, err
	}
	matches, truncated, err := search.FindGlobMatches(r.Path, pattern, search.GlobOpts{
		IncludeDirs: opts.IncludeDirs,
		MaxResults:  opts.MaxResults,
	})
	if err != nil {
		return nil, false, wrapFileError(err, path, "glob")
	}
	out := make([]string, len(matches))
	for i, m := range matches {
		out[i] = sandbox.ReverseResolvePath(s.mounts, m)
	}
	return out, truncated, nil
}

// Grep walks path matching pattern via search.FindGrepMatches.
func (s *Sandbox) Grep(ctx context.Context, path, pattern string, opts sandbox.GrepOpts) ([]sandbox.GrepMatch, bool, error) {
	r, err := resolvePath(s.mounts, path)
	if err != nil {
		return nil, false, err
	}
	matches, truncated, err := search.FindGrepMatches(r.Path, pattern, search.GrepOpts{
		Glob:          opts.Glob,
		Literal:       opts.Literal,
		CaseSensitive: opts.CaseSensitive,
		MaxResults:    opts.MaxResults,
	})
	if err != nil {
		return nil, false, wrapFileError(err, path, "grep")
	}
	out := make([]sandbox.GrepMatch, len(matches))
	for i, m := range matches {
		out[i] = sandbox.GrepMatch{
			Path:       sandbox.ReverseResolvePath(s.mounts, m.Path),
			LineNumber: m.LineNumber,
			Line:       sandbox.MaskHostPathsInOutput(s.mounts, m.Line),
		}
	}
	return out, truncated, nil
}

// wrapFileError maps OS errors to FileNotFoundError / PermissionError / FileError.
func wrapFileError(err error, path, op string) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return sandbox.NewFileNotFoundError(path)
	case errors.Is(err, fs.ErrPermission):
		return sandbox.NewPermissionError(err.Error(), path)
	}
	return sandbox.NewFileError(err.Error(), path, op)
}
