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
func buildNamedSubagents(
	ctx context.Context,
	rt RuntimeContext,
	cfg *config.Config,
	names []string,
) ([]adk.Agent, error) {
	if len(names) == 0 {
		return nil, nil
	}
	out := make([]adk.Agent, 0, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		subSeed := rt
		subSeed.AgentName = name
		subRT, err := NewRuntimeContext(cfg, &subSeed)
		if err != nil {
			slog.Warn(
				"failed to finalize subagent runtime; skipping",
				"agent", name,
				"err", err,
			)
			continue
		}

		sub, _, err := MakeLeadAgent(ctx, subRT, cfg)
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
