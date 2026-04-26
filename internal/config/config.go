package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"eino-cli/internal/config/schema"
)

type Config = schema.Config

type ModelConfig = schema.ModelConfig
type AgentConfig = schema.AgentConfig

const (
	defaultRuntimeModel     = "kimi"
	defaultRuntimeTimeout   = 30
	defaultAgentKey         = "default"
	defaultAgentName        = "deep-agent"
	defaultAgentInstruction = "You are a helpful assistant."
	defaultAgentIterations  = 6
)

func Load() (Config, error) {
	root, err := os.Getwd()
	if err != nil {
		return Config{}, fmt.Errorf("get working directory: %w", err)
	}

	stateDir := filepath.Join(root, ".eino-cli")

	yamlPath := filepath.Join(root, "yaml", "config.yaml")
	yamlModels, err := loadModelsFromYAML(yamlPath)
	if err != nil {
		return Config{}, fmt.Errorf("load yaml config: %w", err)
	}

	cfg := Config{
		RootDir:       root,
		StateDir:      stateDir,
		SessionsDir:   filepath.Join(stateDir, "sessions"),
		MemoryDir:     filepath.Join(stateDir, "memory"),
		CheckpointDir: filepath.Join(stateDir, "checkpoints"),

		RuntimeBaseURL: envOrDefault("EINO_RUNTIME_BASE_URL", ""),
		RuntimeModel:   envOrDefault("EINO_RUNTIME_MODEL", defaultRuntimeModel),
		RuntimeTimeout: envOrDefaultInt("EINO_RUNTIME_TIMEOUT", defaultRuntimeTimeout),

		DefaultModel: defaultYAMLModel,
		DefaultAgent: envOrDefault("EINO_DEFAULT_AGENT", ""),

		Models: yamlModels,
	}

	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return Config{}, err
	}

	if err = ensureDirs(normalized); err != nil {
		return Config{}, err
	}

	return normalized, nil
}

func normalizeConfig(cfg Config) (Config, error) {
	if cfg.RuntimeTimeout <= 0 {
		cfg.RuntimeTimeout = defaultRuntimeTimeout
	}

	if strings.TrimSpace(cfg.DefaultModel) == "" {
		cfg.DefaultModel = strings.TrimSpace(cfg.RuntimeModel)
	}
	if strings.TrimSpace(cfg.DefaultModel) == "" {
		cfg.DefaultModel = defaultRuntimeModel
	}

	if cfg.Models == nil {
		cfg.Models = make(map[string]*ModelConfig)
	}

	defaultModel := strings.TrimSpace(cfg.DefaultModel)
	defaultModelCfg, ok := cfg.Models[defaultModel]
	if !ok {
		defaultModelCfg = &ModelConfig{
			Name:           defaultModel,
			Provider:       envOrDefault("EINO_MODEL_PROVIDER", "claude"),
			Model:          strings.TrimSpace(cfg.RuntimeModel),
			BaseURL:        strings.TrimSpace(cfg.RuntimeBaseURL),
			APIKeyEnv:      envOrDefault("EINO_MODEL_API_KEY_ENV", ""),
			TimeoutSeconds: cfg.RuntimeTimeout,
		}
	}

	if strings.TrimSpace(defaultModelCfg.Model) == "" {
		defaultModelCfg.Model = defaultModel
	}
	if strings.TrimSpace(defaultModelCfg.Name) == "" {
		defaultModelCfg.Name = defaultModel
	}
	if strings.TrimSpace(defaultModelCfg.Provider) == "" {
		defaultModelCfg.Provider = "claude"
	}
	if defaultModelCfg.TimeoutSeconds <= 0 {
		defaultModelCfg.TimeoutSeconds = cfg.RuntimeTimeout
	}
	if strings.TrimSpace(defaultModelCfg.APIKeyEnv) == "" {
		defaultModelCfg.APIKeyEnv = defaultAPIKeyEnv(defaultModelCfg.Provider)
	}

	cfg.Models[defaultModel] = defaultModelCfg
	cfg.RuntimeModel = defaultModel
	if strings.TrimSpace(cfg.RuntimeBaseURL) == "" {
		cfg.RuntimeBaseURL = strings.TrimSpace(defaultModelCfg.BaseURL)
	}

	if strings.TrimSpace(cfg.DefaultAgent) == "" {
		cfg.DefaultAgent = defaultAgentKey
	}
	if cfg.Agents == nil {
		cfg.Agents = map[string]AgentConfig{}
	}

	defaultAgentKey := strings.TrimSpace(cfg.DefaultAgent)
	defaultAgent, ok := cfg.Agents[defaultAgentKey]
	if !ok {
		defaultAgent = AgentConfig{
			Name:         envOrDefault("EINO_AGENT_NAME", defaultAgentName),
			Instruction:  envOrDefault("EINO_AGENT_INSTRUCTION", defaultAgentInstruction),
			MaxIteration: envOrDefaultInt("EINO_AGENT_MAX_ITERATION", defaultAgentIterations),
		}
	}
	if strings.TrimSpace(defaultAgent.Name) == "" {
		defaultAgent.Name = defaultAgentName
	}
	if strings.TrimSpace(defaultAgent.Instruction) == "" {
		defaultAgent.Instruction = defaultAgentInstruction
	}
	if defaultAgent.MaxIteration <= 0 {
		defaultAgent.MaxIteration = defaultAgentIterations
	}
	cfg.Agents[defaultAgentKey] = defaultAgent

	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func validateConfig(cfg Config) error {
	defaultModelKey := strings.TrimSpace(cfg.DefaultModel)
	if defaultModelKey == "" {
		return fmt.Errorf("default model is required")
	}
	modelCfg, ok := cfg.Models[defaultModelKey]
	if !ok {
		return fmt.Errorf("default model %q not found in models", defaultModelKey)
	}
	if strings.TrimSpace(modelCfg.Model) == "" {
		return fmt.Errorf("model %q missing model field", defaultModelKey)
	}
	if strings.TrimSpace(modelCfg.Provider) == "" {
		return fmt.Errorf("model %q missing provider", defaultModelKey)
	}
	if strings.TrimSpace(modelCfg.APIKeyEnv) == "" {
		return fmt.Errorf("model %q missing api key env", defaultModelKey)
	}
	if modelCfg.TimeoutSeconds <= 0 {
		return fmt.Errorf("model %q timeout must be positive", defaultModelKey)
	}

	defaultAgentKey := strings.TrimSpace(cfg.DefaultAgent)
	if defaultAgentKey == "" {
		return fmt.Errorf("default agent is required")
	}
	agentCfg, ok := cfg.Agents[defaultAgentKey]
	if !ok {
		return fmt.Errorf("default agent %q not found in agents", defaultAgentKey)
	}
	if strings.TrimSpace(agentCfg.Name) == "" {
		return fmt.Errorf("agent %q missing name", defaultAgentKey)
	}
	if strings.TrimSpace(agentCfg.Instruction) == "" {
		return fmt.Errorf("agent %q missing instruction", defaultAgentKey)
	}
	if agentCfg.MaxIteration <= 0 {
		return fmt.Errorf("agent %q max iteration must be positive", defaultAgentKey)
	}

	if cfg.RuntimeTimeout <= 0 {
		return fmt.Errorf("runtime timeout must be positive")
	}

	return nil
}

func defaultAPIKeyEnv(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "claude", "anthropic":
		return "ANTHROPIC_API_KEY"
	case "openai":
		return "OPENAI_API_KEY"
	case "kimi", "moonshot":
		return "MOONSHOT_API_KEY"
	default:
		return "ANTHROPIC_API_KEY"
	}
}

func ensureDirs(cfg Config) error {
	for _, dir := range []string{cfg.StateDir, cfg.SessionsDir, cfg.MemoryDir, cfg.CheckpointDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create state directory %s: %w", dir, err)
		}
	}

	return nil
}

func envOrDefault(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func envOrDefaultInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}
