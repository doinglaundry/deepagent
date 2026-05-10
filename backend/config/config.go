package config

import (
	"errors"
	"fmt"
	"log"
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

func Load() (*Config, error) {
	root, err := os.Getwd()
	if err != nil {
		log.Fatalf("get root: %v", err)
	}

	cfg, err := loadFromYAML(filepath.Join(root, "yaml", "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("load yaml config: %w", err)
	}

	cfg.RootDir = root

	defaultModel := strings.TrimSpace(cfg.DefaultModel)
	if defaultModel == "" {
		return nil, fmt.Errorf("default model is empty")
	}

	modelCfg, ok := cfg.Models[defaultModel]
	if !ok || modelCfg == nil {
		return nil, errors.New("default model not found")
	}

	if modelCfg.APIKey == "" {
		modelCfg.APIKey = os.Getenv(defaultAPIKeyEnv(modelCfg.Provider))
	}

	if !isModelConfigValid(modelCfg) {
		return nil, fmt.Errorf("invalid default model: %s", defaultModel)
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
			Model:        defaultModel,
		}
	}

	cfg.Skills = appendDefaultSkillsPath(root, cfg.Skills)

	return cfg, nil
}

func isModelConfigValid(modelCfg *ModelConfig) bool {
	if modelCfg == nil {
		return false
	}
	if strings.TrimSpace(modelCfg.Name) == "" {
		return false
	}

	if strings.TrimSpace(modelCfg.Provider) == "" {
		return false
	}

	if strings.TrimSpace(modelCfg.Model) == "" {
		return false
	}

	if strings.TrimSpace(modelCfg.BaseURL) == "" {
		return false
	}

	return true
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
