package agent

import (
	"context"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/adk"

	"eino-cli/backend/config"
)

// getSubAgents builds the SubAgents slice and the "include
// general-purpose subagent" flag passed to deep.Config.
//
// Subagent dispatch is disabled (returns nil, false, nil) when:
//   - rt.SubagentEnabled is false (host opt-out), OR
//   - the recursion guard is set (we're already building a subagent
//     — depth-1 cap so subagents can't dispatch their own subagents).
//
// Otherwise:
//   - withGeneral = true when the host explicitly enabled it OR didn't
//     configure SubagentsConfig at all (so the model still gets a
//     working task() target by default).
//   - subAgents are built recursively from AppConfig.Subagents.Names;
//     individual build failures are logged-and-skipped inside
//     buildNamedSubagents.
func getSubAgents(
	ctx context.Context,
	rt RuntimeContext,
	cfg config.Config,
	deps AgentDeps,
	appCfg *AppConfig,
) ([]adk.Agent, bool, error) {
	if !rt.SubagentEnabled || isSubagentBuild(ctx) {
		return nil, false, nil
	}
	var subCfg SubagentsConfig
	if appCfg != nil {
		subCfg = appCfg.Subagents
	}
	withGeneral := subCfg.GeneralEnabled || isZeroSubagentsConfig(subCfg)
	built, err := buildNamedSubagents(ctx, rt, cfg, deps, subCfg.Names)
	if err != nil {
		return nil, false, err
	}
	return built, withGeneral, nil
}

// isZeroSubagentsConfig reports whether all SubagentsConfig fields are
// at their zero value. We can't use `==` because the struct contains a
// slice; this helper preserves the "user didn't configure anything"
// detection used to opt into the general-purpose subagent default.
func isZeroSubagentsConfig(c SubagentsConfig) bool {
	return !c.GeneralEnabled && len(c.Names) == 0 && c.MaxConcurrent == 0 && c.MaxPerTurn == 0
}

// subagentBuildKey is a context-only sentinel used to short-circuit
// recursive MakeLeadAgent calls — the second-level call won't try to
// build subagents itself, capping recursion at depth 1. Mirrors
// deerflow's behaviour where subagents are leaves.
type subagentBuildKey struct{}

func withSubagentBuild(ctx context.Context) context.Context {
	return context.WithValue(ctx, subagentBuildKey{}, true)
}

func isSubagentBuild(ctx context.Context) bool {
	v, _ := ctx.Value(subagentBuildKey{}).(bool)
	return v
}

// buildNamedSubagents resolves each name in `names` to an AgentProfile
// and recursively constructs a deep agent for it. The recursive call
// receives a context flagged via withSubagentBuild() so it short-
// circuits its own subagent expansion (depth-1 cap).
//
// A subagent that fails to build is logged-and-skipped rather than
// failing the whole turn — partial subagent availability is preferable
// to a hard error when a sibling agent is misconfigured.
func buildNamedSubagents(
	ctx context.Context,
	rt RuntimeContext,
	cfg config.Config,
	deps AgentDeps,
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
		// Per-subagent runtime: same defaults as the lead, but force
		// SubagentEnabled=false so the recursive deep.New call doesn't
		// also try to wire its own subagents (defence in depth — the
		// context flag does the actual cap).
		subRT := rt
		subRT.AgentName = name
		subRT.SubagentEnabled = false
		subRT.MaxConcurrentSubagents = 0

		sub, err := MakeLeadAgent(withSubagentBuild(ctx), subRT, cfg, deps)
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
