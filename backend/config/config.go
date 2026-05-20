package config

import (
	"errors"
	"fmt"
	"strings"

	"eino-cli/backend/consts"
)

func Load(root string) (config *Config, err error) {

	config, err = loadFromYAML(root)
	if err != nil {
		return nil, fmt.Errorf("load yaml config: %w", err)
	}

	defaultModel := strings.TrimSpace(config.DefaultModel)
	if defaultModel == "" {
		return nil, errors.New("default_model is empty")
	}

	defaultModelCfg, ok := config.Models[defaultModel]
	if !ok || defaultModelCfg == nil {
		return nil, fmt.Errorf("default_model %q not found in models list", defaultModel)
	}

	err = validateModelConfig(defaultModelCfg)
	normalizeSandbox(&config.Sandbox)

	return config, err
}

func normalizeSandbox(s *SandboxConfig) {
	if s.Image == "" {
		s.Image = consts.DefaultSandboxImage
	}
	if s.ContainerPrefix == "" {
		s.ContainerPrefix = consts.DefaultSandboxContainerPrefix
	}
	if s.IdleTimeout == 0 {
		s.IdleTimeout = consts.DefaultSandboxIdleTimeout
	}
	if s.Replicas == 0 {
		s.Replicas = consts.DefaultSandboxReplicas
	}
}

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
