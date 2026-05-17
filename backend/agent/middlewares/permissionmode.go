package middlewares

// PermissionMode mirrors Claude Code's permission modes. Tools consult
// GetPermissionMode(ctx) to decide whether a mutation needs a HITL prompt,
// an outright denial, or a free pass.
type PermissionMode string

const (
	// ModeDefault: every write / shell call goes through HITL approval.
	ModeDefault PermissionMode = "default"

	// ModeAcceptEdits: file mutations auto-approve; shell still asks.
	ModeAcceptEdits PermissionMode = "acceptEdits"

	// ModePlan: read-only mode. Writes (write_file / edit_file /
	// delete_file / execute / shell) reject without prompting.
	ModePlan PermissionMode = "plan"

	// ModeBypass: full trust, no HITL. Use only inside server-side agents
	// where the operator is the sole user.
	ModeBypass PermissionMode = "bypassPermissions"
)

// IsKnownMode rejects typos like "Plan" or "accept_edits" early — the TUI
// reads the user's /command and round-trips through this guard before
// stamping ctx.
func IsKnownMode(m PermissionMode) bool {
	switch m {
	case ModeDefault, ModeAcceptEdits, ModePlan, ModeBypass:
		return true
	}
	return false
}

// IsPlanMode is the common predicate tools use: "should I refuse this
// write?". Cheap helper so individual tools don't repeat the comparison.
func IsPlanMode(m PermissionMode) bool { return m == ModePlan }

// PlanModeDeniedMessage is the response tools return when plan mode
// blocks a write. Centralised so phrasing stays consistent across
// write_file / edit_file / delete_file / execute / shell.
const PlanModeDeniedMessage = "This action is blocked by plan mode. Switch to default or acceptEdits mode to perform writes."
