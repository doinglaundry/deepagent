package agent

import (
	"eino-cli/backend/config"
	"errors"
)

// RuntimeContext is the per-request runtime config threaded through MakeLeadAgent.
type RuntimeContext struct {
	AgentConfig            *config.AgentConfig
	ModelCfg               *config.ModelConfig
	AgentName              string
	IsPlanMode             bool
	SubagentEnabled        bool
	MaxConcurrentSubagents int
	HITLTools              []string
}

// NewRuntimeContext canonicalises rt and returns the resolved agent/model config.
func NewRuntimeContext(
	cfg *config.Config,
	seed *RuntimeContext,
) (RuntimeContext, error) {

	agentName := cfg.DefaultAgent
	if seed != nil && IsValidAgentName(seed.AgentName) {
		agentName = seed.AgentName
	}

	agentConfig, err := GetAgentConfig(cfg, agentName)
	if err != nil {
		return RuntimeContext{}, errors.New("load agent fail")
	}
	if agentConfig == nil {
		return RuntimeContext{}, errors.New("load agent fail")
	}

	modelCfg, err := GetModelConfig(agentConfig.Model, cfg)
	if err != nil {
		return RuntimeContext{}, err
	}

	maxConcurrentSubagents := 3
	if seed != nil && seed.MaxConcurrentSubagents > 0 {
		maxConcurrentSubagents = seed.MaxConcurrentSubagents
	}

	isPlanMode := false //todo cli 传进来
	if seed != nil && seed.IsPlanMode {
		isPlanMode = true
	}

	SubagentEnabled := false
	if seed != nil && seed.SubagentEnabled {
		SubagentEnabled = true
	}

	return RuntimeContext{
		AgentConfig:            agentConfig,
		ModelCfg:               modelCfg,
		AgentName:              agentName,
		IsPlanMode:             isPlanMode,
		SubagentEnabled:        SubagentEnabled,
		MaxConcurrentSubagents: maxConcurrentSubagents,
	}, nil
}
