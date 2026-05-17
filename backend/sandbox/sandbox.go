// Package sandbox declares the Sandbox abstraction the LLM tools see.
package sandbox

import "context"

// Sandbox is the 7-method surface every concrete provider must implement.
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

// GlobOpts are the optional knobs of Sandbox.Glob.
type GlobOpts struct {
	IncludeDirs bool
	MaxResults  int // 0 → impl default (200)
}

// GrepOpts are the optional knobs of Sandbox.Grep.
type GrepOpts struct {
	Glob          string
	Literal       bool
	CaseSensitive bool
	MaxResults    int // 0 → impl default (100)
}

// GrepMatch is one hit reported by Sandbox.Grep.
type GrepMatch struct {
	Path       string
	LineNumber int
	Line       string
}
