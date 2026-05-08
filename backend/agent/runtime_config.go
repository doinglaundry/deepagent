package agent

import (
	"fmt"
	"log/slog"

	"eino-cli/backend/config"
)

// RuntimeContext mirrors the per-request runtime config that the Python
// make_lead_agent extracts from RunnableConfig.configurable + .context. We
// pass it explicitly through the call chain rather than relying on
// langgraph.config.get_config() globals, but the field set is identical.
type RuntimeContext struct {
	// ThinkingEnabled toggles vendor-specific extended-thinking modes.
	// Falls back to false when the resolved model does not support it.
	ThinkingEnabled bool

	// ReasoningEffort is the OpenAI-style "low|medium|high" hint, propagated
	// to providers that surface a reasoning_effort knob.
	ReasoningEffort string

	// ModelName picks a chat model by name from config.Models. Empty falls
	// back through agent_config.Model and finally the global default.
	ModelName string

	// AgentName picks a custom agent profile by name. Empty means "default".
	AgentName string

	// IsPlanMode enables the TodoMiddleware (Phase 3 wires the actual mw;
	// Phase 1 just plumbs the flag through the metadata).
	IsPlanMode bool

	// SubagentEnabled turns on parallel-task orchestration prompt sections
	// and the SubagentLimitMiddleware.
	SubagentEnabled bool

	// MaxConcurrentSubagents is the hard cap on parallel `task` calls per
	// turn. Defaults to 3 (set by defaultRuntimeContext / MergeRuntime).
	MaxConcurrentSubagents int

	// HITLTools lists the tool names that require human approval before
	// the agent may invoke them. Empty (nil or zero-length) means no
	// gating — that is, BuildChain will not attach the HITL middleware
	// at all. Approval prompts are routed through agent.defaultHITLApproval
	// (stdin y/N) so this is a pure declaration of intent; hosts that
	// want different approval UX should attach the middleware themselves.
	HITLTools []string

	// Metadata accumulates trace-tagging key/values (analogous to LangSmith
	// metadata in the Python implementation).
	Metadata map[string]any
}

// NewRuntimeContext returns a fully-finalized RuntimeContext for the
// lead agent: it stamps the hardcoded defaults, seeds AgentName /
// ModelName from cfg's defaults, then runs FinalizeRuntimeContext to
// canonicalize names, resolve the chat model, collapse ThinkingEnabled,
// and emit the "Create Agent" log + Metadata seed.
//
// Production callers (NewDeepAgentRuntime today) should always go
// through this function — it is the single line that gives you a
// ready-to-pass-to-MakeLeadAgent rt.
//
// SubagentEnabled / IsPlanMode are left at the Go zero (false). Hosts
// that want either on flip the field on the returned value before
// calling MakeLeadAgent.
//
// Subagent assembly (buildNamedSubagents) does NOT call this — it
// forks the parent rt, overrides AgentName, and re-runs
// FinalizeRuntimeContext directly. That keeps "fresh rt for the lead"
// and "derived rt for a subagent" as two clearly separate flows.
func NewRuntimeContext(cfg config.Config) (RuntimeContext, error) {
	rt := defaultRuntimeContext()
	rt.AgentName = cfg.DefaultAgent
	rt.ModelName = cfg.DefaultModel
	if err := FinalizeRuntimeContext(&rt, cfg); err != nil {
		return RuntimeContext{}, err
	}
	return rt, nil
}

// defaultRuntimeContext is the hardcoded-defaults seed used internally
// by NewRuntimeContext and exposed (only) to tests in this package
// that need a known starting point without going through cfg-seeding
// or Finalize. The defaults mirror Python's cfg.get(..., default)
// fallbacks.
func defaultRuntimeContext() RuntimeContext {
	return RuntimeContext{
		ThinkingEnabled:        true, // Python: cfg.get("thinking_enabled", True)
		MaxConcurrentSubagents: 3,    // Python: cfg.get("max_concurrent_subagents", 3)
		Metadata:               map[string]any{},
	}
}

// MergeRuntime overlays configurable+context maps onto a RuntimeContext.
// Keys missing from both maps keep the receiver's existing value, matching
// Python's dict.update semantics where context wins over configurable.
func (rt RuntimeContext) MergeRuntime(configurable, context map[string]any) RuntimeContext {
	merged := map[string]any{}
	for k, v := range configurable {
		merged[k] = v
	}
	for k, v := range context {
		merged[k] = v // context overrides configurable (matches Python cfg.update(context))
	}

	if v, ok := boolFrom(merged, "thinking_enabled"); ok {
		rt.ThinkingEnabled = v
	}
	if v, ok := stringFrom(merged, "reasoning_effort"); ok {
		rt.ReasoningEffort = v
	}
	if v, ok := stringFrom(merged, "model_name"); ok {
		rt.ModelName = v
	} else if v, ok := stringFrom(merged, "model"); ok {
		// Python: cfg.get("model_name") or cfg.get("model")
		rt.ModelName = v
	}
	if v, ok := stringFrom(merged, "agent_name"); ok {
		rt.AgentName = v
	}
	if v, ok := boolFrom(merged, "is_plan_mode"); ok {
		rt.IsPlanMode = v
	}
	if v, ok := boolFrom(merged, "subagent_enabled"); ok {
		rt.SubagentEnabled = v
	}
	if v, ok := intFrom(merged, "max_concurrent_subagents"); ok && v > 0 {
		rt.MaxConcurrentSubagents = v
	}
	if rt.MaxConcurrentSubagents <= 0 {
		rt.MaxConcurrentSubagents = 3
	}
	if rt.Metadata == nil {
		rt.Metadata = map[string]any{}
	}
	return rt
}

// FinalizeRuntimeContext is the SOLE place rt is mutated against cfg.
// After it returns, rt is canonical and downstream callers (MakeLeadAgent,
// BuildChain, the prompt assembler, the deep.Config builder) treat rt
// as immutable input.
//
// What it does, in order:
//
//  1. Validate rt.AgentName, write the canonical form back.
//  2. Look up the agent profile so cascading model resolution can read
//     agent_config.Model. Errors propagate.
//  3. Resolve rt.ModelName via the rt → agent.Model → cfg.DefaultModel
//     cascade and write the resolved name back.
//  4. Collapse rt.ThinkingEnabled into the resolved boolean (intent AND
//     model supports thinking). Emits a slog.Warn on downgrade.
//  5. Emit the "Create Agent" line and seed rt.Metadata with the
//     post-resolution values so middleware / renderers downstream see
//     the same view as the log.
//
// The split with MakeLeadAgent is now: this function freezes rt, that
// function consumes rt. Tests and subagent recursion call Finalize too
// (each subagent gets its own canonical rt).
func FinalizeRuntimeContext(rt *RuntimeContext, cfg config.Config) error {
	agentName, err := ValidateAgentName(rt.AgentName)
	if err != nil {
		return err
	}
	rt.AgentName = agentName

	agentConfig, err := GetAgentConfig(cfg, agentName)
	if err != nil {
		return fmt.Errorf("load agent profile %q: %w", agentName, err)
	}

	modelName, modelCfg, err := GetModelConfig(rt.ModelName, agentConfig, cfg)
	if err != nil {
		return err
	}
	rt.ModelName = modelName
	rt.ThinkingEnabled = getThinkingEnabled(rt.ThinkingEnabled, modelCfg, modelName)

	resolvedName := fallback(rt.AgentName, "default")
	resolvedModel := fallback(rt.ModelName, "default")
	slog.Info("Create Agent",
		"agent_name", resolvedName,
		"thinking_enabled", rt.ThinkingEnabled,
		"reasoning_effort", rt.ReasoningEffort,
		"model_name", resolvedModel,
		"is_plan_mode", rt.IsPlanMode,
		"subagent_enabled", rt.SubagentEnabled,
		"max_concurrent_subagents", rt.MaxConcurrentSubagents,
	)

	if rt.Metadata == nil {
		rt.Metadata = map[string]any{}
	}
	rt.Metadata["agent_name"] = resolvedName
	rt.Metadata["model_name"] = resolvedModel
	rt.Metadata["thinking_enabled"] = rt.ThinkingEnabled
	rt.Metadata["reasoning_effort"] = rt.ReasoningEffort
	rt.Metadata["is_plan_mode"] = rt.IsPlanMode
	rt.Metadata["subagent_enabled"] = rt.SubagentEnabled
	if agentConfig != nil {
		rt.Metadata["tool_groups"] = agentConfig.ToolGroups
		if agentConfig.Skills != nil {
			rt.Metadata["available_skills"] = agentConfig.Skills
		}
	}
	return nil
}

func boolFrom(m map[string]any, k string) (bool, bool) {
	v, ok := m[k]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func stringFrom(m map[string]any, k string) (string, bool) {
	v, ok := m[k]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", false
	}
	return s, true
}

func intFrom(m map[string]any, k string) (int, bool) {
	v, ok := m[k]
	if !ok {
		return 0, false
	}
	switch x := v.(type) {
	case int:
		return x, true
	case int32:
		return int(x), true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	}
	return 0, false
}
