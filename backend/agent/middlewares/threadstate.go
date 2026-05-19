package middlewares

import (
	"context"
	"sync/atomic"
)

// struct{} key types avoid collision with other packages' context keys.
type (
	threadIDKey          struct{}
	sandboxIDKey         struct{}
	permissionModeKey    struct{}
	rollbackProtectedKey struct{}
	rollbackPolicyKey    struct{}
)

type RollbackPolicyState struct {
	unsafeToolBlocked atomic.Bool
}

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

func WithRollbackProtected(ctx context.Context, on bool) context.Context {
	return context.WithValue(ctx, rollbackProtectedKey{}, on)
}

func IsRollbackProtected(ctx context.Context) bool {
	v, _ := ctx.Value(rollbackProtectedKey{}).(bool)
	return v
}

func WithRollbackPolicyState(ctx context.Context, state *RollbackPolicyState) context.Context {
	return context.WithValue(ctx, rollbackPolicyKey{}, state)
}

func MarkRollbackUnsafeToolBlocked(ctx context.Context) {
	if state, ok := ctx.Value(rollbackPolicyKey{}).(*RollbackPolicyState); ok && state != nil {
		state.unsafeToolBlocked.Store(true)
	}
}

func WasRollbackUnsafeToolBlocked(state *RollbackPolicyState) bool {
	return state != nil && state.unsafeToolBlocked.Load()
}
