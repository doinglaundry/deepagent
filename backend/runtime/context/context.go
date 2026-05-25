package context

import (
	stdctx "context"
	"sync/atomic"

	"eino-cli/backend/consts"
)

type PermissionMode = consts.PermissionMode
type QuerySource string

const (
	QuerySourceMain      QuerySource = "main"
	QuerySourceAutoDream QuerySource = "auto_dream"
)

type (
	sessionIDKey         struct{}
	sandboxIDKey         struct{}
	permissionModeKey    struct{}
	rollbackProtectedKey struct{}
	rollbackPolicyKey    struct{}
	querySourceKey       struct{}
)

type RollbackPolicyState struct {
	unsafeToolBlocked atomic.Bool
}

func IsKnownMode(m PermissionMode) bool {
	switch m {
	case consts.ModeDefault, consts.ModeAcceptEdits, consts.ModePlan, consts.ModeBypass:
		return true
	}
	return false
}

func WithSessionID(ctx stdctx.Context, sid string) stdctx.Context {
	return stdctx.WithValue(ctx, sessionIDKey{}, sid)
}

func GetSessionID(ctx stdctx.Context) string {
	v, _ := ctx.Value(sessionIDKey{}).(string)
	return v
}

func WithSandboxID(ctx stdctx.Context, sid string) stdctx.Context {
	return stdctx.WithValue(ctx, sandboxIDKey{}, sid)
}

func GetSandboxID(ctx stdctx.Context) string {
	v, _ := ctx.Value(sandboxIDKey{}).(string)
	return v
}

func WithQuerySource(ctx stdctx.Context, source QuerySource) stdctx.Context {
	return stdctx.WithValue(ctx, querySourceKey{}, source)
}

func GetQuerySource(ctx stdctx.Context) QuerySource {
	v, ok := ctx.Value(querySourceKey{}).(QuerySource)
	if !ok {
		return QuerySourceMain
	}
	return v
}

func WithPermissionMode(ctx stdctx.Context, mode PermissionMode) stdctx.Context {
	return stdctx.WithValue(ctx, permissionModeKey{}, mode)
}

func GetPermissionMode(ctx stdctx.Context) PermissionMode {
	v, ok := ctx.Value(permissionModeKey{}).(PermissionMode)
	if !ok {
		return consts.ModeDefault
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
