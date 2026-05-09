package agent

import (
	"eino-cli/backend/config"
	"fmt"
)

// RuntimeContext is the per-request runtime config threaded through MakeLeadAgent.
type RuntimeContext struct {
	ThinkingEnabled        bool
	ReasoningEffort        string
	ModelName              string
	AgentName              string
	IsPlanMode             bool
	SubagentEnabled        bool
	MaxConcurrentSubagents int
	HITLTools              []string
	Metadata               map[string]any
}

// NewRuntimeContext canonicalises rt: stamps defaults (when seed is nil),
// validates the agent name, resolves the chat model + ThinkingEnabled, and
// snapshots Metadata. After it returns rt is treated as immutable input.
func NewRuntimeContext(cfg config.Config, seed *RuntimeContext) (RuntimeContext, error) {
	var rt RuntimeContext
	if seed != nil {
		rt = *seed
	} else {
		rt = RuntimeContext{
			ThinkingEnabled:        true,
			MaxConcurrentSubagents: 3,
			Metadata:               map[string]any{},
			AgentName:              cfg.DefaultAgent,
			ModelName:              cfg.DefaultModel,
		}
	}

	agentName, err := ValidateAgentName(rt.AgentName)
	if err != nil {
		return RuntimeContext{}, err
	}
	rt.AgentName = agentName

	agentConfig, err := GetAgentConfig(cfg, agentName)
	if err != nil {
		return RuntimeContext{}, fmt.Errorf("load agent profile %q: %w", agentName, err)
	}

	modelName, modelCfg, err := GetModelConfig(rt.ModelName, agentConfig, cfg)
	if err != nil {
		return RuntimeContext{}, err
	}
	rt.ModelName = modelName
	rt.ThinkingEnabled = getThinkingEnabled(rt.ThinkingEnabled, modelCfg, modelName)

	resolvedName := fallback(rt.AgentName, "default")
	resolvedModel := fallback(rt.ModelName, "default")

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
	return rt, nil
}

// MergeRuntime overlays configurable+context onto rt; context wins over configurable.
func (rt RuntimeContext) MergeRuntime(configurable, context map[string]any) RuntimeContext {
	merged := map[string]any{}
	for k, v := range configurable {
		merged[k] = v
	}
	for k, v := range context {
		merged[k] = v
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
