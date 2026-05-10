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

	configPath := filepath.Join(root, "yaml", "config.yaml")
	cfg, err := loadFromYAML(configPath)
	if err != nil {
		return Config{}, fmt.Errorf("load yaml config: %w", err)
	}

	cfg.RootDir = root

	normalized, err := normalizeConfig(*cfg)
	if err != nil {
		return Config{}, err
	}

	return normalized, nil
}

// normalizeConfig is the sole place post-load invariants are enforced;
// downstream code trusts the returned Config without re-validation.
func normalizeConfig(cfg Config) (Config, error) {
	defaultModel := strings.TrimSpace(cfg.DefaultModel)
	modelCfg, ok := cfg.Models[defaultModel]
	if !ok || modelCfg == nil {
		return cfg, errors.New("default model not found")
	}
	if modelCfg.APIKey == "" {
		modelCfg.APIKey = os.Getenv(defaultAPIKeyEnv(modelCfg.Provider))
	}

	if strings.TrimSpace(cfg.DefaultAgent) == "" {
		cfg.DefaultAgent = defaultAgentKey
	}

	if cfg.Agents == nil {
		cfg.Agents = map[string]AgentConfig{}
	}
	if _, ok := cfg.Agents[cfg.DefaultAgent]; !ok {
		cfg.Agents[cfg.DefaultAgent] = AgentConfig{
			Name:         defaultAgentName,
			Instruction:  defaultAgentInstruction,
			MaxIteration: defaultAgentIterations,
		}
	}

	cfg.Skills = appendDefaultSkillsPath(cfg.RootDir, cfg.Skills)

	return cfg, nil
}

func appendDefaultSkillsPath(rootDir string, skillsCfg SkillsConfig) SkillsConfig {
	if rootDir == "" {
		return skillsCfg
	}
	skillPath := filepath.Join(rootDir, "backend", "skills")
	info, err := os.Stat(skillPath)
	if err != nil || !info.IsDir() {
		return skillsCfg
	}
	for _, existing := range skillsCfg.Paths {
		if existing == skillPath {
			return skillsCfg
		}
	}
	skillsCfg.Paths = append(skillsCfg.Paths, skillPath)
	return skillsCfg
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

