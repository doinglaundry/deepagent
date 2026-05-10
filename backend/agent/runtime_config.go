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

// NewRuntimeContext returns a baseline *RuntimeContext resolved from
// cfg.DefaultAgent. Callers that want to override AgentName / plan mode /
// subagent settings call SetXxx on the returned pointer.
//
// Returns *RuntimeContext (not value) because callers are expected to mutate
// it via setters; by-value would silently make those mutations local-only.
func NewRuntimeContext(cfg *config.Config) (*RuntimeContext, error) {
	agentName := cfg.DefaultAgent

	agentConfig, err := GetAgentConfig(cfg, agentName)
	if err != nil || agentConfig == nil {
		return nil, errors.New("load agent fail")
	}
	modelCfg, err := GetModelConfig(agentConfig.Model, cfg)
	if err != nil {
		return nil, err
	}

	return &RuntimeContext{
		AgentName:              agentName,
		AgentConfig:            agentConfig,
		ModelCfg:               modelCfg,
		MaxConcurrentSubagents: 3,
	}, nil
}

// Clone returns an independent copy of rt suitable for forking subagents.
// HITLTools is deep-copied because subsequent SetHITLTools on either side
// would otherwise alias through the shared backing array. AgentConfig /
// ModelCfg pointers are shared on purpose — they're effectively immutable
// lookup results owned by *config.Config; SetAgentName replaces the pointer,
// never mutates the pointee.
func (rt *RuntimeContext) Clone() *RuntimeContext {
	clone := *rt
	if rt.HITLTools != nil {
		clone.HITLTools = append([]string(nil), rt.HITLTools...)
	}
	return &clone
}

// SetAgentName re-resolves AgentConfig and ModelCfg against cfg for the new
// name. The three fields (AgentName / AgentConfig / ModelCfg) update
// atomically: on error nothing is touched, so rt stays consistent with its
// previous agent. cfg is passed in (rather than stored on RuntimeContext) so
// the same RuntimeContext can be re-pointed at a different config without
// dragging stale config around.
func (rt *RuntimeContext) SetAgentName(cfg *config.Config, name string) error {
	agentConfig, err := GetAgentConfig(cfg, name)
	if err != nil || agentConfig == nil {
		return errors.New("load agent fail")
	}
	modelCfg, err := GetModelConfig(agentConfig.Model, cfg)
	if err != nil {
		return err
	}
	rt.AgentName = name
	rt.AgentConfig = agentConfig
	rt.ModelCfg = modelCfg
	return nil
}

// SetPlanMode flips IsPlanMode in place. Callers must guarantee no other
// goroutine is reading rt while this runs — DeepAgentRuntime owns that
// guarantee via its own mu, and lead agent code never holds onto rt after
// MakeLeadAgent returns.
func (rt *RuntimeContext) SetPlanMode(plan bool) { rt.IsPlanMode = plan }

func (rt *RuntimeContext) SetSubagentEnabled(enabled bool) { rt.SubagentEnabled = enabled }

// SetMaxConcurrentSubagents normalises non-positive n back to the baseline
// default 3 — same default NewRuntimeContext seeds — so callers don't have
// to special-case "I want the default".
func (rt *RuntimeContext) SetMaxConcurrentSubagents(n int) {
	if n <= 0 {
		n = 3
	}
	rt.MaxConcurrentSubagents = n
}

func (rt *RuntimeContext) SetHITLTools(tools []string) { rt.HITLTools = tools }
