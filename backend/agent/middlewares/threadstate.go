package middlewares

import "context"

// struct{} key types avoid collision with other packages' context keys.
type (
	threadIDKey       struct{}
	sandboxIDKey      struct{}
	permissionModeKey struct{}
)

// WithThreadID stamps tid on ctx; empty tid falls back to the generic sandbox.
func WithThreadID(ctx context.Context, tid string) context.Context {
	return context.WithValue(ctx, threadIDKey{}, tid)
}

// GetThreadID returns the stamped tid, or "" when none is set.
func GetThreadID(ctx context.Context) string {
	v, _ := ctx.Value(threadIDKey{}).(string)
	return v
}

// WithSandboxID stamps sid on ctx so tools can resolve the live sandbox.
func WithSandboxID(ctx context.Context, sid string) context.Context {
	return context.WithValue(ctx, sandboxIDKey{}, sid)
}

// GetSandboxID returns the stamped sid, or "" when none is set.
func GetSandboxID(ctx context.Context) string {
	v, _ := ctx.Value(sandboxIDKey{}).(string)
	return v
}

// WithPermissionMode stamps mode on ctx so tools can gate writes uniformly.
func WithPermissionMode(ctx context.Context, mode PermissionMode) context.Context {
	return context.WithValue(ctx, permissionModeKey{}, mode)
}

// GetPermissionMode returns the mode on ctx; default is ModeDefault.
func GetPermissionMode(ctx context.Context) PermissionMode {
	v, ok := ctx.Value(permissionModeKey{}).(PermissionMode)
	if !ok {
		return ModeDefault
	}
	return v
}
