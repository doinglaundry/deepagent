package agent

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"eino-cli/backend/config"
)

// agentNamePattern is the validation rule for agent_name: letters, digits, dash, underscore.
var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func IsValidAgentName(agentName string) bool {
	return agentNamePattern.MatchString(strings.TrimSpace(agentName))
}

// ValidateAgentName returns the trimmed name (or "" for empty/default sentinel).
// Returns error when the name contains illegal characters.
func ValidateAgentName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", nil
	}
	if !agentNamePattern.MatchString(trimmed) {
		return "", fmt.Errorf("invalid agent_name %q: must match %s", trimmed, agentNamePattern.String())
	}
	return trimmed, nil
}

func GetModelConfig(modelName string, cfg *config.Config) (*config.ModelConfig, error) {
	if strings.TrimSpace(modelName) == "" {
		return nil, errors.New("no model name provided")
	}

	modelConfig, exists := cfg.Models[modelName]
	if !exists {
		return nil, errors.New("no model config found")
	}

	return modelConfig, nil
}
