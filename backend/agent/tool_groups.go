package agent

import (
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/prebuilt/deep"

	"eino-cli/backend/config"
)

// applyToolGroups maps profile.ToolGroups to deep.Config's Backend / Shell
// switches. nil = inherit all; explicit slice opts in; unknown groups warn-and-ignore.
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
