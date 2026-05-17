// Package sandbox declares the Sandbox abstraction the LLM tools see.
// Concrete implementations live in the local/ and aio/ subpackages and plug
// in via the SandboxManager interface (manager.go) selected by factory.go.
package sandbox

import "context"

// Sandbox mirrors deerflow.sandbox.sandbox.Sandbox — 7 methods the tool
// layer needs. Method set is stable; new operations go through a new
// interface to avoid the "every implementation must re-export the world"
// trap.
type Sandbox interface {
	ID() string

	ExecuteCommand(ctx context.Context, cmd string) (string, error)

	ReadFile(ctx context.Context, path string) (string, error)
	WriteFile(ctx context.Context, path, content string, appendMode bool) error
	UpdateFile(ctx context.Context, path string, content []byte) error

	ListDir(ctx context.Context, path string, maxDepth int) ([]string, error)
	Glob(ctx context.Context, path, pattern string, opts GlobOpts) ([]string, bool, error)
	Grep(ctx context.Context, path, pattern string, opts GrepOpts) ([]GrepMatch, bool, error)
}

// GlobOpts groups the optional knobs of Glob so the interface stays at one
// line per method and new options don't break implementers.
type GlobOpts struct {
	IncludeDirs bool
	MaxResults  int // 0 → defaults at the implementation (200 for deer-flow parity)
}

// GrepOpts: same rationale.
type GrepOpts struct {
	Glob          string // optional sub-path filter
	Literal       bool   // treat pattern as a literal string, not regex
	CaseSensitive bool
	MaxResults    int // 0 → default 100
}

// GrepMatch: path + line number + truncated line content. Path is whatever
// the sandbox reports — local sandbox reverse-resolves it to /mnt/... so
// the LLM only ever sees container-shaped paths.
type GrepMatch struct {
	Path       string
	LineNumber int
	Line       string
}
