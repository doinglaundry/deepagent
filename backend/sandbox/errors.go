// Package sandbox: errors mirror deer-flow's deerflow.sandbox.exceptions
// hierarchy so handlers and tools can `errors.As` on the same categories
// regardless of which manager (local / aio) raised them.
package sandbox

import (
	"errors"
	"fmt"
	"strings"
)

// baseErr is the embedded common state. Named with lowercase so it doesn't
// collide with the Error() method on every exported subtype.
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

// Message returns the raw message without the (key=value) suffix.
func (e *baseErr) Message() string { return e.msg }

// Details returns the structured payload (sandbox_id, path, exit_code...)
// observability sinks attach without parsing the string form.
func (e *baseErr) Details() map[string]any { return e.details }

// NotFoundError: sandbox id has no live instance.
type NotFoundError struct {
	baseErr
	SandboxID string
}

func NewNotFoundError(sandboxID string) *NotFoundError {
	return &NotFoundError{
		baseErr:   baseErr{msg: "sandbox not found", details: map[string]any{"sandbox_id": sandboxID}},
		SandboxID: sandboxID,
	}
}

// RuntimeError: container runtime / external dependency misconfigured.
type RuntimeError struct{ baseErr }

func NewRuntimeError(msg string) *RuntimeError {
	return &RuntimeError{baseErr: baseErr{msg: msg}}
}

// CommandError: shell exit != 0 or sandbox-side exec failure.
type CommandError struct {
	baseErr
	Command  string
	ExitCode int
}

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

// FileError: read/write/update failed. Path and operation surface in
// details so observability sees "which tool / which path" without log
// parsing.
type FileError struct {
	baseErr
	Path      string
	Operation string
}

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

// PermissionError: read-only mount / path-escape / mode-denied write.
// Wraps FileError so handlers can errors.As on either.
type PermissionError struct{ FileError }

func NewPermissionError(msg, path string) *PermissionError {
	return &PermissionError{FileError: *NewFileError(msg, path, "write")}
}

// FileNotFoundError: stat / open returned ENOENT.
type FileNotFoundError struct{ FileError }

func NewFileNotFoundError(path string) *FileNotFoundError {
	return &FileNotFoundError{FileError: *NewFileError("file not found", path, "read")}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// Sentinel checks for fast `errors.Is` style use sites.
var (
	ErrSandboxNotFound  = errors.New("sandbox not found")
	ErrThreadIDRequired = errors.New("thread_id required")
)
