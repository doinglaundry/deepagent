package agent

import (
	"github.com/cloudwego/eino/adk/filesystem"
)

// SandboxProvider abstracts the host's sandboxed file/shell surface so the
// agent layer doesn't have to know whether it is talking to a local
// directory, a Docker container, an aio-sandbox session, or a remote ACP
// workspace. Mirrors deerflow.sandbox.SandboxProvider.
//
// The contract is intentionally tight: the agent assembly only needs to ask
// for a Backend (file ops + grep/glob), a Shell (command execution), and
// the list of any Mount entries that should appear in the prompt's
// "Custom Mounted Directories" section.
type SandboxProvider interface {
	// Backend returns the filesystem backend the deep agent's tools use.
	// Must be non-nil.
	Backend() filesystem.Backend

	// Shell returns the shell the deep agent's bash tool will invoke. Must
	// be non-nil.
	Shell() filesystem.Shell

	// Mounts returns the custom mounts to surface in the system prompt.
	// May be empty; the prompt section is only emitted when len > 0.
	Mounts() []Mount

	// WorkingDir returns the agent's logical CWD for tools that resolve
	// relative paths. May be empty when the provider doesn't expose one.
	WorkingDir() string
}
