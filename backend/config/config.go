package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultRuntimeModel     = "kimi"
	defaultRuntimeTimeout   = 30
	defaultAgentKey         = "default"
	defaultAgentName        = "deep-agent"
	defaultAgentInstruction = "You are a helpful assistant. You have access to filesystem tools (read files, list directories, search file contents, write and edit files) and a shell for running commands. Use these tools proactively to answer questions and complete tasks. For internet access, use the shell tool to run curl commands."
	defaultAgentIterations  = 6
)

func Load() (Config, error) {
	root, err := os.Getwd()
	if err != nil {
		return Config{}, fmt.Errorf("get working directory: %w", err)
	}

	persistenceDir := filepath.Join(root, ".eino-cli")

	configPath := filepath.Join(root, "yaml", "config.yaml")
	yamlModels, yamlExtras, err := loadFromYAML(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("load yaml config: %w", err)
	}

	cfg := Config{
		RootDir:        root,
		PersistenceDir: persistenceDir,
		SessionsDir:    filepath.Join(persistenceDir, "sessions"),
		MemoryDir:      filepath.Join(persistenceDir, "memory"),
		CheckpointDir:  filepath.Join(persistenceDir, "checkpoints"),

		RuntimeModel:   envOrDefault("EINO_RUNTIME_MODEL", defaultRuntimeModel),
		RuntimeTimeout: envOrDefaultInt("EINO_RUNTIME_TIMEOUT", defaultRuntimeTimeout),

		// DefaultModel comes from the built-in fallback for now;
		// yamlFileConfig.DefaultModel exists to mirror the YAML
		// schema but is not wired through (default is hardcoded
		// to "kimi" in defaultYAMLModel).
		DefaultModel: defaultYAMLModel,
		// DefaultAgent has no YAML source — env override or
		// built-in default ("default") chosen in normalizeConfig.
		DefaultAgent: envOrDefault("EINO_DEFAULT_AGENT", ""),

		Models: yamlModels,

		Skills:     yamlExtras.Skills,
		ToolSearch: yamlExtras.ToolSearch,
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
	// Fill missing fields for the default model loaded from YAML.
	defaultModel := strings.TrimSpace(cfg.DefaultModel)
	mc := cfg.Models[defaultModel]
	if strings.TrimSpace(mc.Name) == "" {
		mc.Name = defaultModel
	}
	if strings.TrimSpace(mc.Model) == "" {
		mc.Model = defaultModel
	}
	if strings.TrimSpace(mc.Provider) == "" {
		mc.Provider = "kimi"
	}
	if mc.TimeoutSeconds <= 0 {
		mc.TimeoutSeconds = cfg.RuntimeTimeout
	}
	// Only fall back to the provider-default env var when the user
	// hasn't supplied a literal key OR an explicit env-var name.
	// Filling in APIKeyEnv unconditionally would clobber a literal
	// key in the validateConfig "either-or" check below.
	if strings.TrimSpace(mc.APIKey) == "" && strings.TrimSpace(mc.APIKeyEnv) == "" {
		mc.APIKeyEnv = defaultAPIKeyEnv(mc.Provider)
	}
	cfg.Models[defaultModel] = mc
	cfg.RuntimeModel = defaultModel

	// Build default agent config (agents are not loaded from YAML).
	if cfg.Agents == nil {
		cfg.Agents = map[string]AgentConfig{}
	}
	if strings.TrimSpace(cfg.DefaultAgent) == "" {
		cfg.DefaultAgent = defaultAgentKey
	}
	agentKey := strings.TrimSpace(cfg.DefaultAgent)
	agent, ok := cfg.Agents[agentKey]
	if !ok {
		agent = AgentConfig{
			Name:         envOrDefault("EINO_AGENT_NAME", defaultAgentName),
			Instruction:  envOrDefault("EINO_AGENT_INSTRUCTION", defaultAgentInstruction),
			MaxIteration: envOrDefaultInt("EINO_AGENT_MAX_ITERATION", defaultAgentIterations),
		}
	}
	if strings.TrimSpace(agent.Name) == "" {
		agent.Name = defaultAgentName
	}
	if strings.TrimSpace(agent.Instruction) == "" {
		agent.Instruction = defaultAgentInstruction
	}
	if agent.MaxIteration <= 0 {
		agent.MaxIteration = defaultAgentIterations
	}
	cfg.Agents[agentKey] = agent

	cfg.Skills = appendDefaultSkillsPath(cfg.RootDir, cfg.Skills)

	return cfg, validateConfig(cfg)
}

// appendDefaultSkillsPath wires up the vendored skill catalog at
// $RootDir/backend/skills as a default scan target. Mirrors deerflow's
// "skills root sits next to backend/" convention without hard-coding
// the absolute path: if the directory exists at runtime we add it,
// otherwise we silently skip so deployments without the vendored
// catalog (lighter container images, CI smoke configs) keep working.
//
// The path is appended (not prepended) so any user-configured paths
// in config.yaml retain priority for same-name overrides — matches
// loader.LoadFromPaths' "first occurrence wins for flat layouts"
// dedup rule.
func appendDefaultSkillsPath(rootDir string, sc SkillsConfig) SkillsConfig {
	if rootDir == "" {
		return sc
	}
	candidate := filepath.Join(rootDir, "backend", "skills")
	info, err := os.Stat(candidate)
	if err != nil || !info.IsDir() {
		return sc
	}
	for _, existing := range sc.Paths {
		if existing == candidate {
			return sc
		}
	}
	sc.Paths = append(sc.Paths, candidate)
	return sc
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
	// Either a literal api_key or an api_key_env (env-var indirection)
	// must be present. The YAML loader normalises one or the other —
	// validateConfig only re-asserts it.
	if strings.TrimSpace(modelCfg.APIKey) == "" && strings.TrimSpace(modelCfg.APIKeyEnv) == "" {
		return fmt.Errorf("model %q missing api_key (literal or $ENV form)", defaultModelKey)
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
	for _, dir := range []string{cfg.PersistenceDir, cfg.SessionsDir, cfg.MemoryDir, cfg.CheckpointDir} {
		err := os.MkdirAll(dir, 0o755)
		if err != nil {
			return fmt.Errorf("create persistence directory %s: %w", dir, err)
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
