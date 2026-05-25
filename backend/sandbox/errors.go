package sandbox

import (
	"errors"
	"fmt"
	"strings"
)

// baseErr keeps the field name lowercase so it doesn't shadow Error() on subtypes.
type baseErr struct {
	msg     string
	details map[string]any
}

func (e *baseErr) Error() string {
	if len(e.details) == 0 {
		return e.msg
	}
	parts := make([]string, 0, len(e.details))
	for k, v := range e.details {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return fmt.Sprintf("%s (%s)", e.msg, strings.Join(parts, ", "))
}

// Message returns the raw message without the details suffix.
func (e *baseErr) Message() string { return e.msg }

// Details returns the structured payload observability sinks consume.
func (e *baseErr) Details() map[string]any { return e.details }

// NotFoundError signals a sandbox id with no live instance.
type NotFoundError struct {
	baseErr
	SandboxID string
}

// NewNotFoundError builds a NotFoundError for sandboxID.
func NewNotFoundError(sandboxID string) *NotFoundError {
	return &NotFoundError{
		baseErr:   baseErr{msg: "sandbox not found", details: map[string]any{"sandbox_id": sandboxID}},
		SandboxID: sandboxID,
	}
}

// RuntimeError signals a misconfigured runtime / external dependency.
type RuntimeError struct{ baseErr }

// NewRuntimeError builds a RuntimeError with msg.
func NewRuntimeError(msg string) *RuntimeError {
	return &RuntimeError{baseErr: baseErr{msg: msg}}
}

// CommandError signals a non-zero exit or sandbox-side exec failure.
type CommandError struct {
	baseErr
	Command  string
	ExitCode int
}

// NewCommandError builds a CommandError; cmd is truncated to 100 chars in details.
func NewCommandError(msg, cmd string, exitCode int) *CommandError {
	details := map[string]any{"exit_code": exitCode}
	if cmd != "" {
		details["command"] = truncate(cmd, 100)
	}
	return &CommandError{
		baseErr:  baseErr{msg: msg, details: details},
		Command:  cmd,
		ExitCode: exitCode,
	}
}

// FileError signals a failed read/write/update operation.
type FileError struct {
	baseErr
	Path      string
	Operation string
}

// NewFileError builds a FileError tagged with path and operation.
func NewFileError(msg, path, op string) *FileError {
	details := map[string]any{}
	if path != "" {
		details["path"] = path
	}
	if op != "" {
		details["operation"] = op
	}
	return &FileError{baseErr: baseErr{msg: msg, details: details}, Path: path, Operation: op}
}

// PermissionError wraps FileError so handlers can errors.As on either.
type PermissionError struct{ FileError }

// NewPermissionError builds a PermissionError tagged at path.
func NewPermissionError(msg, path string) *PermissionError {
	return &PermissionError{FileError: *NewFileError(msg, path, "write")}
}

// FileNotFoundError signals ENOENT on stat/open.
type FileNotFoundError struct{ FileError }

// NewFileNotFoundError builds a FileNotFoundError at path.
func NewFileNotFoundError(path string) *FileNotFoundError {
	return &FileNotFoundError{FileError: *NewFileError("file not found", path, "read")}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// Sentinels for errors.Is.
var (
	ErrSandboxNotFound  = errors.New("sandbox not found")
	ErrSessionIDRequired = errors.New("session_id required")
)
