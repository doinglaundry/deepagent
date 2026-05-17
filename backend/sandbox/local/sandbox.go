// Package local implements Sandbox on the host fs with per-thread path mappings.
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
)

const commandTimeout = 10 * time.Minute

// Sandbox is the per-thread (or generic "local") sandbox instance.
type Sandbox struct {
	id       string
	mappings []PathMapping

	writtenMu sync.RWMutex
	written   map[string]struct{}
}

func newSandbox(id string, mappings []PathMapping) *Sandbox {
	return &Sandbox{
		id:       id,
		mappings: mappings,
		written:  map[string]struct{}{},
	}
}

// ID returns the sandbox id.
func (s *Sandbox) ID() string { return s.id }

// ExecuteCommand runs cmd through a host shell, rewriting /mnt/* in/out.
func (s *Sandbox) ExecuteCommand(ctx context.Context, cmd string) (string, error) {
	resolved := resolvePathsInCommand(s.mappings, cmd)
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

	out := stdout
	if stderr != "" {
		if out != "" {
			out += "\nStd Error:\n" + stderr
		} else {
			out = stderr
		}
	}
	if exitCode != 0 {
		out += fmt.Sprintf("\nExit Code: %d", exitCode)
	}
	if out == "" {
		out = "(no output)"
	}
	out = reverseResolvePathsInOutput(s.mappings, out)
	if runErr != nil && exitCode == 0 {
		return out, sandbox.NewCommandError(runErr.Error(), cmd, exitCode)
	}
	return out, nil
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
	r, err := resolvePath(s.mappings, path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(r.Path)
	if err != nil {
		return "", wrapFileError(err, path, "read")
	}
	content := string(data)
	s.writtenMu.RLock()
	_, agentWritten := s.written[r.Path]
	s.writtenMu.RUnlock()
	if agentWritten {
		content = reverseResolvePathsInOutput(s.mappings, content)
	}
	return content, nil
}

// WriteFile creates/overwrites/appends; read-only mounts surface as PermissionError.
func (s *Sandbox) WriteFile(ctx context.Context, path, content string, appendMode bool) error {
	r, err := resolvePath(s.mappings, path)
	if err != nil {
		return err
	}
	if r.Mapping != nil && r.Mapping.ReadOnly {
		return sandbox.NewPermissionError("read-only file system", path)
	}
	if isReadOnlyPath(s.mappings, r.Path) {
		return sandbox.NewPermissionError("read-only file system", path)
	}
	if dir := filepath.Dir(r.Path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return wrapFileError(err, path, "write")
		}
	}
	resolvedContent := resolvePathsInContent(s.mappings, content)

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
	if _, err := f.WriteString(resolvedContent); err != nil {
		return wrapFileError(err, path, "write")
	}

	s.writtenMu.Lock()
	s.written[r.Path] = struct{}{}
	s.writtenMu.Unlock()
	return nil
}

// UpdateFile is the binary overwrite path; never registers agent-written (no strings to mask).
func (s *Sandbox) UpdateFile(ctx context.Context, path string, content []byte) error {
	r, err := resolvePath(s.mappings, path)
	if err != nil {
		return err
	}
	if r.Mapping != nil && r.Mapping.ReadOnly {
		return sandbox.NewPermissionError("read-only file system", path)
	}
	if isReadOnlyPath(s.mappings, r.Path) {
		return sandbox.NewPermissionError("read-only file system", path)
	}
	if dir := filepath.Dir(r.Path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return wrapFileError(err, path, "update")
		}
	}
	if err := os.WriteFile(r.Path, content, 0o644); err != nil {
		return wrapFileError(err, path, "update")
	}
	return nil
}

// ListDir returns entries under path up to maxDepth (default 2).
func (s *Sandbox) ListDir(ctx context.Context, path string, maxDepth int) ([]string, error) {
	if maxDepth <= 0 {
		maxDepth = 2
	}
	r, err := resolvePath(s.mappings, path)
	if err != nil {
		return nil, err
	}
	entries, err := listDir(r.Path, maxDepth)
	if err != nil {
		return nil, wrapFileError(err, path, "list")
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		isDir := strings.HasSuffix(e, "/") || strings.HasSuffix(e, `\`)
		stripped := strings.TrimRight(e, `/\`)
		reversed := reverseResolvePath(s.mappings, stripped)
		if isDir && !strings.HasSuffix(reversed, "/") {
			reversed += "/"
		}
		out = append(out, reversed)
	}
	return out, nil
}

// Glob walks path matching pattern via search.FindGlobMatches.
func (s *Sandbox) Glob(ctx context.Context, path, pattern string, opts sandbox.GlobOpts) ([]string, bool, error) {
	r, err := resolvePath(s.mappings, path)
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
		out[i] = reverseResolvePath(s.mappings, m)
	}
	return out, truncated, nil
}

// Grep walks path matching pattern via search.FindGrepMatches.
func (s *Sandbox) Grep(ctx context.Context, path, pattern string, opts sandbox.GrepOpts) ([]sandbox.GrepMatch, bool, error) {
	r, err := resolvePath(s.mappings, path)
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
			Path:       reverseResolvePath(s.mappings, m.Path),
			LineNumber: m.LineNumber,
			Line:       m.Line,
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
