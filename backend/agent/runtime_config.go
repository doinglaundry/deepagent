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
}

// NewRuntimeContext canonicalises rt: stamps defaults, validates the agent name,
// resolves the chat model + ThinkingEnabled, then treats rt as immutable input.
func NewRuntimeContext(cfg config.Config, seed *RuntimeContext) (RuntimeContext, error) {
	var rt RuntimeContext
	if seed != nil {
		rt = *seed
	} else {
		rt = RuntimeContext{
			ThinkingEnabled:        true,
			MaxConcurrentSubagents: 3,
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

	return rt, nil
}
