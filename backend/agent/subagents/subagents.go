// Package subagents will host the registry + concrete sub-agents
// (general_purpose, bash_agent, …) that deer-flow uses for "spawn a
// scoped helper, gather its result, return to the lead agent" flows.
//
// Today this is a stub — the lead agent surface in M0..M4 stayed flat
// because adding sub-agents is orthogonal to the sandboxing /
// multi-tenancy work (issue scoped to a separate change). When the
// sub-agent work lands it will define:
//
//   - Spec: kind ("general_purpose"|"bash_agent"|…), name, system prompt,
//     tool subset, model override.
//   - Registry: name -> Spec, plus the "subagent" tool the lead agent
//     calls to dispatch.
//   - Runner: per-invocation goroutine that boots a fresh agent.Runner,
//     pipes its output back through SandboxMiddleware so the sub-agent
//     sees the same sandbox the lead has.
package subagents

// Spec is the future shape — kept as a placeholder so callers can
// import the package without "no symbols" warnings.
type Spec struct {
	Name        string
	Description string
}
