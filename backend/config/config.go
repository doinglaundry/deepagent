package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	defaultSandboxImage           = "enterprise-public-cn-beijing.cr.volces.com/vefaas-public/all-in-one-sandbox:latest"
	defaultSandboxContainerPrefix = "deer-flow-sandbox"
	defaultSandboxIdleTimeout     = 10 * time.Minute
	defaultSandboxReplicas        = 3
)

const (
	defaultAgentKey         = "default"
	defaultAgentInstruction = "You are a helpful assistant. You have access to filesystem tools (read files, list directories, search file contents, write and edit files) and a shell for running commands. Use these tools proactively to answer questions and complete tasks. For internet access, use the shell tool to run curl commands."
	defaultAgentIterations  = 50
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

	normalizeSandbox(&config.Sandbox)

	return
}

// normalizeSandbox fills zero-valued sandbox fields with built-in defaults.
func normalizeSandbox(s *SandboxConfig) {
	if s.Image == "" {
		s.Image = defaultSandboxImage
	}
	if s.ContainerPrefix == "" {
		s.ContainerPrefix = defaultSandboxContainerPrefix
	}
	if s.IdleTimeout == 0 {
		s.IdleTimeout = defaultSandboxIdleTimeout
	}
	if s.Replicas == 0 {
		s.Replicas = defaultSandboxReplicas
	}
}

// CompleteDefaultModelConfig validates that cfg.DefaultModel exists and
// has every required field. API-key resolution is NOT done here; that is
// normalizeModels' job (api_key_env → api_key:$VAR → literal). Keeping
// the resolution single-sourced is what stops drift bugs like "yaml
// says ARK_API_KEY but the second-pass fallback re-reads OPENAI_API_KEY
// and silently masks an empty key".
func CompleteDefaultModelConfig(cfg *Config) error {
	defaultModel := strings.TrimSpace(cfg.DefaultModel)
	if defaultModel == "" {
		return errors.New("default_model is empty")
	}

	defaultModelCfg, ok := cfg.Models[defaultModel]
	if !ok || defaultModelCfg == nil {
		return fmt.Errorf("default_model %q not found in models list", defaultModel)
	}

	if err := validateModelConfig(defaultModelCfg); err != nil {
		return fmt.Errorf("default_model %q: %w", defaultModel, err)
	}

	return nil
}

// validateModelConfig surfaces empty required fields with names so the
// startup error tells the user exactly which yaml field to fix instead
// of letting an empty key reach the chat completion endpoint and
// produce a misleading 401.
func validateModelConfig(m *ModelConfig) error {
	if m == nil {
		return errors.New("model config is nil")
	}
	switch {
	case strings.TrimSpace(m.Name) == "":
		return errors.New("name is empty")
	case strings.TrimSpace(m.Provider) == "":
		return errors.New("provider is empty")
	case strings.TrimSpace(m.Model) == "":
		return errors.New("model is empty")
	case strings.TrimSpace(m.BaseURL) == "":
		return errors.New("base_url is empty")
	case strings.TrimSpace(m.APIKey) == "":
		return errors.New("api_key is empty (set api_key, api_key_env, or api_key: $VAR in yaml/config.yaml AND export the env var if you used api_key_env)")
	}
	return nil
}
