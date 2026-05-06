package agent

import (
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"eino-cli/backend/config"
)

// AgentProfile mirrors deerflow.config.agents_config.AgentConfig — the
// "custom agent" descriptor loaded per agent_name. Phase 1 wires the type
// only; the actual on-disk loader lands in Phase 3 once the YAML schema is
// finalized.
type AgentProfile struct {
	Name        string
	Model       string   // overrides the global default model when set
	ToolGroups  []string // restricts available tools when set
	Skills      []string // nil → inherit; non-nil (even empty) → strict subset
	Instruction string
	// MaxIteration mirrors deerflow's AgentConfig.max_iteration: the
	// per-turn cap on agent loop steps. 0 means "use the runtime default"
	// (defaultIterationLimit returns 6 in that case).
	MaxIteration int
}

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

// LoadAgentConfig is the Phase-1 stub kept around for tests that exercise
// the "no custom profile" branch without plumbing a config.Config. New
// call sites should use LoadAgentProfile(cfg, name) which actually reads
// the inline agents block + on-disk agents/<name>/config.yaml.
//
// Deprecated: use LoadAgentProfile(cfg, name).
func LoadAgentConfig(name string) *AgentProfile {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	return nil
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

// GetModelForAgent picks the effective model name following the Python
// chain `request → agent_config.model → global default` and returns the
// resolved ModelConfig pointer alongside it.
func GetModelForAgent(requested string, profile *AgentProfile, cfg config.Config) (string, *config.ModelConfig, error) {
	candidate := requested
	if candidate == "" && profile != nil {
		candidate = profile.Model
	}
	name, err := GetModelName(candidate, cfg)
	if err != nil {
		return "", nil, err
	}
	mc := cfg.Models[name]
	if mc == nil {
		return "", nil, fmt.Errorf("no chat model could be resolved: model %q missing from cfg.Models", name)
	}
	return name, mc, nil
}
