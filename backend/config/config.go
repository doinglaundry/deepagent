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

	err = isModelConfigValid(defaultModelCfg)
	if err != nil {
		return nil, err
	}

	if config.Sandbox.Image == "" {
		config.Sandbox.Image = consts.DefaultSandboxImage
	}
	if config.Sandbox.ContainerPrefix == "" {
		config.Sandbox.ContainerPrefix = consts.DefaultSandboxContainerPrefix
	}
	if config.Sandbox.IdleTimeout == 0 {
		config.Sandbox.IdleTimeout = consts.DefaultSandboxIdleTimeout
	}
	if config.Sandbox.Replicas == 0 {
		config.Sandbox.Replicas = consts.DefaultSandboxReplicas
	}
	return
}

func isModelConfigValid(m *ModelConfig) error {
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
