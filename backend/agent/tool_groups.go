package agent

import (
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/prebuilt/deep"

	"eino-cli/backend/config"
)

// applyToolGroups is the Go counterpart of deerflow's
// get_available_tools(groups=profile.tool_groups) filter. The deep.New
// surface is coarser than Python's per-tool registry — Backend!=nil
// triggers ALL filesystem tools as a unit, Shell!=nil triggers the
// execute tool — so we collapse Python's fine-grained group list to
// the two switches eino exposes.
//
// nil ToolGroups (Python's None) means "no filter, inherit all": both
// Backend and Shell are wired through. An explicit slice opts into
// specific groups; unknown groups are logged-and-ignored rather than
// failing, so a config that mentions web_search / mcp / other groups
// not yet wired up in Go still loads (with reduced capability instead
// of an error). An empty slice means "no built-in tools at all".
//
// Callers pass the backend and shell directly — there used to be a
// SandboxProvider abstraction in front of these, but it was deleted
// when only the local impl was ever wired in.
func applyToolGroups(cfg *deep.Config, profile *config.AgentConfig, backend filesystem.Backend, shell filesystem.Shell) {
	if profile == nil || profile.ToolGroups == nil {
		cfg.Backend = backend
		cfg.Shell = shell
		return
	}
	enabledFS := false
	enabledShell := false
	for _, g := range profile.ToolGroups {
		switch strings.ToLower(strings.TrimSpace(g)) {
		case "":
			continue
		case "filesystem", "files", "file":
			enabledFS = true
		case "shell", "bash", "execute":
			enabledShell = true
		default:
			slog.Info(
				"agent profile tool_group is not wired in Go runtime; ignoring",
				"agent", profile.Name,
				"group", g,
			)
		}
	}
	if enabledFS {
		cfg.Backend = backend
	}
	if enabledShell {
		cfg.Shell = shell
	}
}
