package middlewares

import "context"

// Context keys used to thread (UserID, ThreadID, SandboxID, PermissionMode)
// down to tools. struct{} ensures no collision with other packages' keys.
type (
	threadIDKey       struct{}
	sandboxIDKey      struct{}
	permissionModeKey struct{}
)

// WithThreadID stamps the thread id on ctx so SandboxMiddleware (and
// downstream tools that need /mnt/user-data/<thread>/ resolution) can pull
// it back. Empty tid is allowed — middlewares fall back to the generic
// sandbox.
func WithThreadID(ctx context.Context, tid string) context.Context {
	return context.WithValue(ctx, threadIDKey{}, tid)
}

// GetThreadID returns the stamped thread id, or "" when none is set.
func GetThreadID(ctx context.Context) string {
	v, _ := ctx.Value(threadIDKey{}).(string)
	return v
}

// WithSandboxID is called by SandboxMiddleware after Acquire so subsequent
// tool calls in the same run see the live sid via GetSandboxID.
func WithSandboxID(ctx context.Context, sid string) context.Context {
	return context.WithValue(ctx, sandboxIDKey{}, sid)
}

func GetSandboxID(ctx context.Context) string {
	v, _ := ctx.Value(sandboxIDKey{}).(string)
	return v
}

// WithPermissionMode threads the user-facing mode (default / accept-edits /
// plan / bypass-permissions) so tools can refuse writes in plan mode without
// each tool having to read the config.
func WithPermissionMode(ctx context.Context, mode PermissionMode) context.Context {
	return context.WithValue(ctx, permissionModeKey{}, mode)
}

// GetPermissionMode returns the mode on ctx, defaulting to ModeDefault when
// none is set — equivalent to "ask before every mutation".
func GetPermissionMode(ctx context.Context) PermissionMode {
	v, ok := ctx.Value(permissionModeKey{}).(PermissionMode)
	if !ok {
		return ModeDefault
	}
	return v
}
