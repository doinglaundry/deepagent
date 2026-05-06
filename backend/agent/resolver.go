package agent

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"eino-cli/backend/config"
)

// The custom-agent descriptor used at runtime is config.AgentConfig.
// We deliberately do NOT define a parallel "AgentProfile" / domain
// type here — the field set is identical to the on-disk schema and
// runtime never derives anything from it, so a wrapper would be pure
// boilerplate (plus a Description-shaped trap: any field added to the
// schema would silently get dropped at the runtime boundary).
//
// Precedent: *config.ModelConfig is also passed straight through from
// the loader to the runtime. Same shape here.

// agentNamePattern mirrors the validation rule used by the Python
// validate_agent_name: lower/upper letters, digits, dash, underscore.
var agentNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// ValidateAgentName mirrors deerflow.config.agents_config.validate_agent_name.
// Returns "" for empty input (the Python "default" sentinel) and an error for
// names that contain illegal characters.
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

// GetModelName mirrors deerflow.agents.lead_agent.agent._resolve_model_name.
//
// Resolution order:
//  1. requested name, if it exists in cfg.Models
//  2. fall back to the global default (cfg.DefaultModel)
//
// Falls back with a slog.Warn when the requested name is non-empty but
// missing — matching Python's logger.warning behaviour.
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
	mc := cfg.Models[name]
	if mc == nil {
		return "", nil, fmt.Errorf("no chat model could be resolved: model %q missing from cfg.Models", name)
	}
	return name, mc, nil
}
