package agent

import (
	"context"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/adk"

	"eino-cli/backend/config"
)

// generalSubagentEnabled reports whether the deep agent should expose
// the built-in general-purpose subagent (a clone of the lead with a
// generic instruction). Subagent dispatch as a whole is gated on
// rt.SubagentEnabled + the recursion guard; this helper just answers
// "include the general-purpose target?" within that gate.
//
// Today the answer is always true once the gate is open: the dedicated
// SubagentsConfig knob is gone (it was always zero in production), so
// the general-purpose target is the only target — anything else would
// leave the model with a `task()` tool that can't be invoked.
func generalSubagentEnabled(ctx context.Context, rt RuntimeContext) bool {
	if !rt.SubagentEnabled || isSubagentBuild(ctx) {
		return false
	}
	return true
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

// buildNamedSubagents resolves each name in `names` to a config.AgentConfig
// and recursively constructs a deep agent for it. The recursive call
// receives a context flagged via withSubagentBuild() so it short-
// circuits its own subagent expansion (depth-1 cap).
//
// A subagent that fails to build is logged-and-skipped rather than
// failing the whole turn — partial subagent availability is preferable
// to a hard error when a sibling agent is misconfigured.
//
// Currently no caller passes non-empty names — production wiring
// always relies on the general-purpose subagent. The function is kept
// for future yaml-driven named subagent dispatch.
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
