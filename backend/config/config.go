package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	defaultAgentKey         = "default"
	defaultAgentInstruction = "You are a helpful assistant. You have access to filesystem tools (read files, list directories, search file contents, write and edit files) and a shell for running commands. Use these tools proactively to answer questions and complete tasks. For internet access, use the shell tool to run curl commands."
	defaultAgentIterations  = 6
)

func Load(root string) (config *Config, err error) {

	config, err = loadFromYAML(root)
	if err != nil {
		return nil, fmt.Errorf("load yaml config: %w", err)
	}

	err = CompleteDefaultModelConfig(config)
	if err != nil {
		return nil, fmt.Errorf("complete default model config: %w", err)
	}

	return
}

func CompleteDefaultModelConfig(config *Config) error {
	defaultModel := strings.TrimSpace(config.DefaultModel)
	if defaultModel == "" {
		return fmt.Errorf("default model is empty")
	}

	defaultModelCfg, ok := config.Models[defaultModel]
	if !ok || defaultModelCfg == nil {
		return errors.New("default model not found")
	}

	if defaultModelCfg.APIKey == "" {
		defaultModelCfg.APIKey = os.Getenv(GetAPIEnvKey(defaultModelCfg.Provider))
	}

	if !isModelConfigValid(defaultModelCfg) {
		return errors.New("invalid default model config")
	}

	return nil

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

func GetAPIEnvKey(provider string) string {
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
