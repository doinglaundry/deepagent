package tools

import (
	"context"
	"fmt"

	"eino-cli/backend/consts"
	runtimecontext "eino-cli/backend/runtime/context"
	"eino-cli/backend/sandbox"
)

func hasSandboxManager(manager sandbox.SandboxManager) bool {
	return manager != nil
}

// getSandbox returns nil when no manager or sid is available; callers fall back to host fs.
func getSandbox(ctx context.Context, manager sandbox.SandboxManager) sandbox.Sandbox {
	if manager == nil {
		return nil
	}
	sid := runtimecontext.GetSandboxID(ctx)
	if sid == "" {
		return nil
	}
	sb, err := manager.Get(ctx, sid)
	if err != nil {
		return nil
	}
	return sb
}

func getRequiredSandbox(ctx context.Context, manager sandbox.SandboxManager) (sandbox.Sandbox, error) {
	if manager == nil {
		return nil, fmt.Errorf("sandbox manager is not configured")
	}
	sid := runtimecontext.GetSandboxID(ctx)
	if sid == "" {
		sessionID := runtimecontext.GetSessionID(ctx)
		if sessionID == "" {
			sessionID = consts.DefaultSessionID
		}
		var err error
		sid, err = manager.GetSandboxIdBySessionId(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("acquire sandbox: %w", err)
		}
	}
	sb, err := manager.Get(ctx, sid)
	if err != nil {
		return nil, fmt.Errorf("get sandbox %s: %w", sid, err)
	}
	return sb, nil
}

func allowsIsolatedExec(manager sandbox.SandboxManager) bool {
	return manager != nil && manager.AllowsIsolatedExec()
}

// denyOnPlanMode returns (msg, true) when ctx is in plan mode; write tools short-circuit on true.
func denyOnPlanMode(ctx context.Context) (string, bool) {
	if runtimecontext.GetPermissionMode(ctx) == consts.ModePlan {
		return consts.PlanModeDeniedMessage, true
	}
	return "", false
}

func denyOnRollbackProtected(ctx context.Context) (string, bool) {
	if runtimecontext.IsRollbackProtected(ctx) {
		runtimecontext.MarkRollbackUnsafeToolBlocked(ctx)
		return "This tool is disabled in rollback-protected runs because shell side effects cannot be restored safely.", true
	}
	return "", false
}
