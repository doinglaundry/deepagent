package agent

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
	// turn. Defaults to 3 in NewRuntimeContext if unset.
	MaxConcurrentSubagents int

	// Metadata accumulates trace-tagging key/values (analogous to LangSmith
	// metadata in the Python implementation).
	Metadata map[string]any
}

// NewRuntimeContext returns a RuntimeContext with the same defaults the
// Python make_lead_agent applies via cfg.get(..., default).
func NewRuntimeContext() RuntimeContext {
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
