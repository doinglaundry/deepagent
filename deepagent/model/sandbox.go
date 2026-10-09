package model

import (
	"context"
)

type SandboxManager interface {
	SessionID() string
	GetSandboxIdBySessionId(ctx context.Context, sessionID string) (string, error)
	Get(ctx context.Context, sandboxID string) (Sandbox, error)
	Release(ctx context.Context, sandboxID string) error

	Reset()
	UsesSessionDataMounts() bool
	AllowsIsolatedExec() bool
}

type Shutdowner interface{ Shutdown() }

type SandboxFileInfo struct {
	Path      string
	IsDir     bool
	IsSymlink bool
	Size      int64
}

// FileInfoProvider is optional metadata support for concrete container filesystems.
type FileInfoProvider interface {
	ListDirInfo(context.Context, string, int) ([]SandboxFileInfo, error)
}

// ContainerPathResolver resolves symlinks inside a container before a workspace
// file operation is sent to its file API.
type ContainerPathResolver interface {
	ResolveContainerPath(context.Context, string) (string, error)
}

// Sandbox defines the file and command capabilities of a sandbox provider.
type Sandbox interface {
	ID() string
	SessionID() string

	ExecuteCommand(ctx context.Context, cmd string) (string, error)

	ReadFile(ctx context.Context, path string) (string, error)
	WriteFile(ctx context.Context, path, content string, appendMode bool) error
	UpdateFile(ctx context.Context, path string, content []byte) error

	ListDir(ctx context.Context, path string, maxDepth int) ([]string, error)
	Glob(ctx context.Context, path, pattern string, opts SandboxGlobOptions) ([]string, bool, error)
	Grep(ctx context.Context, path, pattern string, opts SandboxGrepOptions) ([]SandboxGrepMatch, bool, error)
}

// SandboxGlobOptions controls Sandbox.Glob.
type SandboxGlobOptions struct {
	IncludeDirs bool
	MaxResults  int // 0 → impl default (200)
}

// SandboxGrepOptions controls Sandbox.Grep.
type SandboxGrepOptions struct {
	Glob          string
	Literal       bool
	CaseSensitive bool
	MaxResults    int // 0 → impl default (100)
}

// SandboxGrepMatch is one hit reported by Sandbox.Grep.
type SandboxGrepMatch struct {
	Path       string
	LineNumber int
	Line       string
}
