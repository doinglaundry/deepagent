package agent

import (
	"context"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/adk"

	"eino-cli/backend/config"
)

// generalSubagentEnabled reports whether to expose the built-in subagent.
func generalSubagentEnabled(rt RuntimeContext) bool {
	return rt.SubagentEnabled
}

// subagentBuildKey caps MakeLeadAgent recursion at depth 1: the second-level
// call observes this sentinel and skips its own subagent expansion.
type subagentBuildKey struct{}

func withSubagentBuild(ctx context.Context) context.Context {
	return context.WithValue(ctx, subagentBuildKey{}, true)
}

func isSubagentBuild(ctx context.Context) bool {
	v, _ := ctx.Value(subagentBuildKey{}).(bool)
	return v
}

// buildNamedSubagents resolves each name to an agent profile and recursively
// builds a deep agent. Failures are logged-and-skipped (partial > hard error).
func buildNamedSubagents(
	ctx context.Context,
	rt RuntimeContext,
	cfg config.Config,
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
		subRT, agentConfig, modelCfg, err := NewRuntimeContext(cfg, &subSeed)
		if err != nil {
			slog.Warn(
				"failed to finalize subagent runtime; skipping",
				"agent", name,
				"err", err,
			)
			continue
		}

		sub, _, err := MakeLeadAgent(withSubagentBuild(ctx), subRT, cfg, agentConfig, modelCfg)
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
