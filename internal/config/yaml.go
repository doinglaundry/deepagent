package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"eino-cli/internal/config/schema"
	"gopkg.in/yaml.v3"
)

type yamlModelEntry struct {
	Name           string  `yaml:"name"`
	Provider       string  `yaml:"provider"`
	Model          string  `yaml:"model"`
	BaseURL        string  `yaml:"base_url"`
	APIBase        string  `yaml:"api_base"`
	APIKey         string  `yaml:"api_key"`
	APIKeyEnv      string  `yaml:"api_key_env"`
	Timeout        float64 `yaml:"timeout"`
	TimeoutSeconds int     `yaml:"timeout_seconds"`
}

type yamlFileConfig struct {
	DefaultModel string           `yaml:"default_model"`
	Models       []yamlModelEntry `yaml:"models"`
}

func loadModelsFromYAML(path string) (map[string]schema.ModelConfig, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", nil
		}
		return nil, "", fmt.Errorf("read yaml config: %w", err)
	}
	var fc yamlFileConfig
	if err := yaml.Unmarshal(data, &fc); err != nil {
		return nil, "", fmt.Errorf("parse yaml config: %w", err)
	}
	if len(fc.Models) == 0 {
		return nil, "", nil
	}

	models := make(map[string]schema.ModelConfig, len(fc.Models))
	for _, m := range fc.Models {
		mc := schema.ModelConfig{
			Name:     m.Name,
			Provider: m.Provider,
			Model:    m.Model,
		}
		if m.BaseURL != "" {
			mc.BaseURL = m.BaseURL
		} else if m.APIBase != "" {
			mc.BaseURL = m.APIBase
		}
		if m.APIKeyEnv != "" {
			mc.APIKeyEnv = m.APIKeyEnv
		} else if v, ok := strings.CutPrefix(m.APIKey, "$"); ok {
			mc.APIKeyEnv = v
		}
		if m.TimeoutSeconds > 0 {
			mc.TimeoutSeconds = m.TimeoutSeconds
		} else if m.Timeout > 0 {
			mc.TimeoutSeconds = int(m.Timeout)
		}
		if mc.Provider == "" {
			mc.Provider = inferProvider(mc.BaseURL, m.Name)
		}
		models[m.Name] = mc
	}
	return models, fc.DefaultModel, nil
}

func inferProvider(baseURL, name string) string {
	lower := strings.ToLower(baseURL + name)
	switch {
	case strings.Contains(lower, "moonshot") || strings.Contains(lower, "kimi"):
		return "kimi"
	case strings.Contains(lower, "openai"):
		return "openai"
	case strings.Contains(lower, "anthropic") || strings.Contains(lower, "claude"):
		return "claude"
	default:
		return "openai"
	}
}
