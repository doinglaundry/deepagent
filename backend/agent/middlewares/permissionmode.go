package middlewares

// PermissionMode mirrors Claude Code's permission modes.
type PermissionMode string

const (
	ModeDefault     PermissionMode = "default"           // HITL on every mutation
	ModeAcceptEdits PermissionMode = "acceptEdits"       // writes auto-approve, shell still asks
	ModePlan        PermissionMode = "plan"              // read-only; writes hard-deny
	ModeBypass      PermissionMode = "bypassPermissions" // full trust, no HITL
)

// IsKnownMode rejects typos like "Plan" or "accept_edits".
func IsKnownMode(m PermissionMode) bool {
	switch m {
	case ModeDefault, ModeAcceptEdits, ModePlan, ModeBypass:
		return true
	}
	return false
}

// IsPlanMode reports whether m is the read-only plan mode.
func IsPlanMode(m PermissionMode) bool { return m == ModePlan }

// PlanModeDeniedMessage is the canned refusal write tools return in plan mode.
const PlanModeDeniedMessage = "This action is blocked by plan mode. Switch to default or acceptEdits mode to perform writes."
