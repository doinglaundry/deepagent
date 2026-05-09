package agent

import (
	"fmt"
	"strings"

	"eino-cli/backend/config"
)

func GetAgentConfig(cfg config.Config, name string) (*config.AgentConfig, error) {
	name, err := ValidateAgentName(name)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, nil
	}
	agentCfg, ok := cfg.Agents[name]
	if !ok {
		return nil, fmt.Errorf("agent %q not found in cfg.Agents", name)
	}
	agentCfg.Name = nameOrFallback(agentCfg.Name, name)
	agentCfg.Model = strings.TrimSpace(agentCfg.Model)
	return &agentCfg, nil
}

func nameOrFallback(candidate, fallback string) string {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}
