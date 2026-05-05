package agent

import (
	"context"

	"eino-cli/backend/agent/middlewares"
)

// RuntimeExtras gathers every optional runtime-supplied dependency the
// host (REPL / runtime/eino) wants to inject into the agent. Bundling them
// into a single value keeps NewDeepAgentRuntime's signature stable as we
// add more knobs over time and avoids the "long parameter list smell".
//
// All fields default to no-op behaviour when zero — the agent runs with
// the same prompt + chain it shipped with before any of these extras
// were available.
type RuntimeExtras struct {
	PromptDeps *PromptDeps
	AppConfig  *AppConfig

	// DeferredToolNames feeds the DeferredTools middleware. nil leaves
	// the middleware unattached even if AppConfig.ToolSearch.Enabled is
	// true — both flags must agree before the gating activates.
	DeferredToolNames func() []string

	// HITLApproval is the synchronous approval callback. nil falls back
	// to "approve everything" inside the HITL middleware.
	HITLApproval func(ctx context.Context, toolName, args string) bool

	// HITLTools is the gated tool-name allowlist. Empty disables HITL
	// gating entirely (no middleware attached).
	HITLTools []string

	// OnClarification is invoked whenever the Clarification middleware
	// rewrites an ask_clarification call. The rewrite happens regardless
	// — this is purely a host-observable hook.
	OnClarification func(ctx context.Context, question string)

	// MemoryHooks drives the Memory middleware's inject / extract data
	// plane. Wire only when AppConfig.Memory.Enabled is true.
	MemoryHooks middlewares.MemoryHooks

	// MemoryFlushHook is the deerflow-style memory_flush_hook plugged
	// into the summarization middleware. Optional; nil means "no
	// flush hook".
	MemoryFlushHook middlewares.SummarizationMemoryFlushHook
}

// ApplyTo overlays the non-nil / non-empty fields of r onto deps and
// returns the result. Zero-valued fields are intentionally ignored so
// the runtime layer's own defaults (Sandbox, WorkingDir, PromptDeps,
// AppConfig) survive when the caller leaves an extra unset.
func (r RuntimeExtras) ApplyTo(deps AgentDeps) AgentDeps {
	if r.PromptDeps != nil {
		deps.PromptDeps = r.PromptDeps
	}
	if r.AppConfig != nil {
		deps.AppConfig = r.AppConfig
	}
	if r.DeferredToolNames != nil {
		deps.DeferredToolNames = r.DeferredToolNames
	}
	if r.HITLApproval != nil {
		deps.HITLApproval = r.HITLApproval
	}
	if len(r.HITLTools) > 0 {
		deps.HITLTools = r.HITLTools
	}
	if r.OnClarification != nil {
		deps.OnClarification = r.OnClarification
	}
	if r.MemoryHooks.Inject != nil || r.MemoryHooks.Extract != nil {
		deps.MemoryHooks = r.MemoryHooks
	}
	if r.MemoryFlushHook != nil {
		deps.MemoryFlushHook = r.MemoryFlushHook
	}
	return deps
}
