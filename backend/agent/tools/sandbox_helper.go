package tools

import (
	"context"
	"strings"

	"eino-cli/backend/consts"
	runtimecontext "eino-cli/backend/runtime/context"
	"eino-cli/backend/sandbox"
)

// Only /mnt/* paths route through the sandbox; host paths stay on os.* fast path.
func shouldUseSandbox(path string) bool {
	return strings.HasPrefix(path, "/mnt/")
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

// denyOnPlanMode returns (msg, true) when ctx is in plan mode; write tools short-circuit on true.
func denyOnPlanMode(ctx context.Context) (string, bool) {
	if runtimecontext.GetPermissionMode(ctx) == runtimecontext.ModePlan {
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
