package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
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
	cfg, err := loadFromYAML(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("load yaml config: %w", err)
	}

	cfg.RootDir = root
	cfg.PersistenceDir = persistenceDir
	cfg.SessionsDir = filepath.Join(persistenceDir, "sessions")
	cfg.MemoryDir = filepath.Join(persistenceDir, "memory")
	cfg.CheckpointDir = filepath.Join(persistenceDir, "checkpoints")

	normalized, err := normalizeConfig(*cfg)
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
	mc, ok := cfg.Models[defaultModel]
	if !ok || mc == nil {
		return cfg, errors.New("default model not found")
	}

	// Fall back to the provider's canonical env when yaml supplied
	// neither api_key nor api_key_env (or the env it pointed at was
	// empty). Lets a minimal `provider: openai` entry just work as
	// long as the user has OPENAI_API_KEY exported. mc is a pointer
	// into cfg.Models, so writing through it updates the map in
	// place.
	if mc.APIKey == "" {
		mc.APIKey = os.Getenv(defaultAPIKeyEnv(mc.Provider))
	}

	if strings.TrimSpace(cfg.DefaultAgent) == "" {
		cfg.DefaultAgent = defaultAgentKey
	}

	if cfg.Agents == nil {
		cfg.Agents = map[string]AgentConfig{
			defaultAgentKey: {
				Name:         defaultAgentName,
				Instruction:  defaultAgentInstruction,
				MaxIteration: defaultAgentIterations,
			},
		}
	}

	cfg.Skills = appendDefaultSkillsPath(cfg.RootDir, cfg.Skills)

	return cfg, nil
}

func appendDefaultSkillsPath(rootDir string, sc SkillsConfig) SkillsConfig {
	if rootDir == "" {
		return sc
	}
	skillPath := filepath.Join(rootDir, "backend", "skills")
	info, err := os.Stat(skillPath)
	if err != nil || !info.IsDir() {
		return sc
	}
	for _, existing := range sc.Paths {
		if existing == skillPath {
			return sc
		}
	}
	sc.Paths = append(sc.Paths, skillPath)
	return sc
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
