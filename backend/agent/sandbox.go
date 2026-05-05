package agent

import (
	"context"

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

// ImageReader is an optional capability a SandboxProvider may also satisfy
// to expose binary image bytes for the ViewImage middleware. Kept on a
// separate interface so providers without image support don't have to
// fake it — the middleware does a runtime type-assertion.
//
// The returned MIMEType should be a valid IANA media type (e.g.
// "image/png"). Implementations should return a wrapped error when the
// path doesn't exist, isn't a regular file, or doesn't look like an image.
type ImageReader interface {
	// ReadImage resolves path under the sandbox root and returns the
	// raw bytes plus the inferred MIME type. The path may be absolute or
	// relative to WorkingDir().
	ReadImage(ctx context.Context, path string) (data []byte, mime string, err error)
}
