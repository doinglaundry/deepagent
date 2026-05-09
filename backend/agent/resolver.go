package agent

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"eino-cli/backend/config"
)

// agentNamePattern is the validation rule for agent_name: letters, digits, dash, underscore.
var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

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

// GetModelName returns requested if present in cfg.Models, else cfg.DefaultModel.
// A non-empty-but-missing requested falls through with a Warn.
func GetModelName(requested string, cfg config.Config) (string, error) {
	defaultName := strings.TrimSpace(cfg.DefaultModel)
	if defaultName == "" || cfg.Models[defaultName] == nil {
		return "", fmt.Errorf("no chat models are configured: please configure at least one model in config.yaml")
	}

	requested = strings.TrimSpace(requested)
	if requested != "" {
		if cfg.Models[requested] != nil {
			return requested, nil
		}
		slog.Warn("requested model not found, falling back to default",
			"requested", requested, "default", defaultName)
	}
	return defaultName, nil
}

func GetModelConfig(modelName string, agentConfig *config.AgentConfig, cfg config.Config) (string, *config.ModelConfig, error) {
	if modelName == "" && agentConfig != nil {
		modelName = agentConfig.Model
	}
	name, err := GetModelName(modelName, cfg)
	if err != nil {
		return "", nil, err
	}
	return name, cfg.Models[name], nil
}
