package context

import (
	stdctx "context"
	"sync/atomic"

	"eino-cli/backend/consts"
	"eino-cli/backend/sandbox"
)

type PermissionMode string

const (
	ModeDefault     PermissionMode = consts.ModeDefault
	ModeAcceptEdits PermissionMode = consts.ModeAcceptEdits
	ModePlan        PermissionMode = consts.ModePlan
	ModeBypass      PermissionMode = consts.ModeBypass
)

type (
	threadIDKey          struct{}
	sandboxIDKey         struct{}
	permissionModeKey    struct{}
	rollbackProtectedKey struct{}
	rollbackPolicyKey    struct{}
	sandboxManagerKey    struct{}
)

type RollbackPolicyState struct {
	unsafeToolBlocked atomic.Bool
}

func IsKnownMode(m PermissionMode) bool {
	switch m {
	case ModeDefault, ModeAcceptEdits, ModePlan, ModeBypass:
		return true
	}
	return false
}

func WithThreadID(ctx stdctx.Context, tid string) stdctx.Context {
	return stdctx.WithValue(ctx, threadIDKey{}, tid)
}

func GetThreadID(ctx stdctx.Context) string {
	v, _ := ctx.Value(threadIDKey{}).(string)
	return v
}

func WithSandboxID(ctx stdctx.Context, sid string) stdctx.Context {
	return stdctx.WithValue(ctx, sandboxIDKey{}, sid)
}

func GetSandboxID(ctx stdctx.Context) string {
	v, _ := ctx.Value(sandboxIDKey{}).(string)
	return v
}

func WithSandboxManager(ctx stdctx.Context, manager sandbox.SandboxManager) stdctx.Context {
	return stdctx.WithValue(ctx, sandboxManagerKey{}, manager)
}

func GetSandboxManager(ctx stdctx.Context) sandbox.SandboxManager {
	manager, _ := ctx.Value(sandboxManagerKey{}).(sandbox.SandboxManager)
	return manager
}

func WithPermissionMode(ctx stdctx.Context, mode PermissionMode) stdctx.Context {
	return stdctx.WithValue(ctx, permissionModeKey{}, mode)
}

func GetPermissionMode(ctx stdctx.Context) PermissionMode {
	v, ok := ctx.Value(permissionModeKey{}).(PermissionMode)
	if !ok {
		return ModeDefault
	}
	return v
}

func WithRollbackProtected(ctx stdctx.Context, on bool) stdctx.Context {
	return stdctx.WithValue(ctx, rollbackProtectedKey{}, on)
}

func IsRollbackProtected(ctx stdctx.Context) bool {
	v, _ := ctx.Value(rollbackProtectedKey{}).(bool)
	return v
}

func WithRollbackPolicyState(ctx stdctx.Context, state *RollbackPolicyState) stdctx.Context {
	return stdctx.WithValue(ctx, rollbackPolicyKey{}, state)
}

func MarkRollbackUnsafeToolBlocked(ctx stdctx.Context) {
	if state, ok := ctx.Value(rollbackPolicyKey{}).(*RollbackPolicyState); ok && state != nil {
		state.unsafeToolBlocked.Store(true)
	}
}

func WasRollbackUnsafeToolBlocked(state *RollbackPolicyState) bool {
	return state != nil && state.unsafeToolBlocked.Load()
}
