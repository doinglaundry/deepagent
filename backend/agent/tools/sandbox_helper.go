package tools

import (
	"context"
	"strings"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/sandbox"
)

// shouldUseSandbox: only route paths that look container-mapped through
// the sandbox. CLI-style host paths (/Users/..., /home/...) keep going
// through the legacy os.* path so the dual-mode CLI/server gateway keeps
// working during the migration window.
func shouldUseSandbox(path string) bool {
	return strings.HasPrefix(path, "/mnt/")
}

// sandboxFromCtx pulls the active Sandbox out of ctx, or returns nil when
// no manager / id is wired (CLI mode, tests). Tools call this once at the
// top of their handler and branch on the result.
//
// The two-step lookup (Default manager, then GetSandboxID) is intentional:
//   - SetDefault is process-wide (M3 / M4 wire it once at startup).
//   - GetSandboxID is per-call, set by SandboxMiddleware.BeforeAgent.
//
// Returns nil instead of an error when either side is missing: that keeps
// existing CLI flows backwards-compatible — tools that see nil run their
// pre-M2 host-fs path.
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

// denyOnPlanMode returns the canned plan-mode denial message + true when
// the caller should short-circuit. Write tools wrap their entry-point with
// this helper instead of duplicating the comparison.
func denyOnPlanMode(ctx context.Context) (string, bool) {
	if middlewares.IsPlanMode(middlewares.GetPermissionMode(ctx)) {
		return middlewares.PlanModeDeniedMessage, true
	}
	return "", false
}
