package agent

import (
	"context"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/adk"

	"eino-cli/backend/config"
)

// buildNamedSubagents resolves each name to an agent profile and recursively
// builds a deep agent. Failures are logged-and-skipped (partial > hard error).
//
// Subagents always run with IsPlanMode=false and IsSubagentEnabled=false:
// only the lead orchestrator may plan or fork further subagents, so a
// subagent that itself spawns subagents (and one of THOSE plans) is a
// fanout / scope confusion we explicitly reject.
func buildNamedSubagents(
	ctx context.Context,
	cfg *config.Config,
	names []string,
) ([]adk.Agent, error) {
	if len(names) == 0 {
		return nil, nil
	}
	out := make([]adk.Agent, 0, len(names))
	for _, agentName := range names {
		name := strings.TrimSpace(agentName)
		if name == "" {
			continue
		}
		sub, _, err := MakeLeadAgent(ctx, name, false, false, cfg)
		if err != nil {
			slog.Warn(
				"failed to build subagent; skipping",
				"agent", name,
				"err", err,
			)
			continue
		}
		out = append(out, sub)
	}
	return out, nil
}
