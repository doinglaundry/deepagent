package agent

import (
	"errors"
	"strings"

	"eino-cli/backend/config"
)

func GetAgentConfig(cfg *config.Config, name string) (*config.AgentConfig, error) {
	name, err := ValidateAgentName(name)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, nil
	}
	agentCfg, ok := cfg.Agents[name]
	if !ok {
		return nil, errors.New("agent not found in cfg.Agents")
	}
	if agentCfg.Name != name {
		return nil, errors.New("agent found in cfg.Agents but its name is different")
	}
	return &agentCfg, nil
}

func nameOrFallback(candidate, fallback string) string {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}
