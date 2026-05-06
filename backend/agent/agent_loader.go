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
	ac, ok := cfg.Agents[name]
	if !ok {
		return nil, fmt.Errorf("agent %q not found in cfg.Agents", name)
	}
	ac.Name = nameOrFallback(ac.Name, name)
	ac.Model = strings.TrimSpace(ac.Model)
	return &ac, nil
}

func nameOrFallback(candidate, fallback string) string {
	trimmed := strings.TrimSpace(candidate)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}
