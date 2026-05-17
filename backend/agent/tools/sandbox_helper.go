package tools

import (
	"context"
	"strings"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/sandbox"
)

// Only /mnt/* paths route through the sandbox; host paths stay on os.* fast path.
func shouldUseSandbox(path string) bool {
	return strings.HasPrefix(path, "/mnt/")
}

// sandboxFromCtx returns nil when no manager or no sid — caller falls back to host fs.
func sandboxFromCtx(ctx context.Context) sandbox.Sandbox {
	manager := sandbox.Default()
	if manager == nil {
		return nil
	}
	sid := middlewares.GetSandboxID(ctx)
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
	if middlewares.IsPlanMode(middlewares.GetPermissionMode(ctx)) {
		return middlewares.PlanModeDeniedMessage, true
	}
	return "", false
}
