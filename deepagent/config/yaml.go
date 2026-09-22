package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const defaultYAMLModel = "kimi"

type ModelEntry struct {
	Name           string `yaml:"name"`
	Provider       string `yaml:"provider"`
	Model          string `yaml:"model"`
	BaseURL        string `yaml:"base_url"`
	APIBase        string `yaml:"api_base"`
	APIKey         string `yaml:"api_key"`
	APIKeyEnv      string `yaml:"api_key_env"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

type Memory struct {
	Enabled                   bool    `yaml:"enabled"`
	StoragePath               string  `yaml:"storage_path"`
	DebounceSeconds           int     `yaml:"debounce_seconds"`
	ModelName                 string  `yaml:"model_name"`
	MaxFacts                  int     `yaml:"max_facts"`
	FactConfidenceThreshold   float64 `yaml:"fact_confidence_threshold"`
	InjectionEnabled          bool    `yaml:"injection_enabled"`
	MaxInjectionTokens        int     `yaml:"max_injection_tokens"`
	DedupEnabled              bool    `yaml:"dedup_enabled"`
	EpisodicDefaultTTLSeconds int     `yaml:"episodic_default_ttl_seconds"`
}

type WebSearch struct {
	Enabled        bool   `yaml:"enabled"`
	Provider       string `yaml:"provider"` // bocha (only one supported today)
	BaseURL        string `yaml:"base_url"`
	APIKey         string `yaml:"api_key"`
	APIKeyEnv      string `yaml:"api_key_env"`
	MaxResults     int    `yaml:"max_results"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
}

type ErrorHandling struct {
	Enabled        bool                 `yaml:"enabled"`
	Retry          RetryConfig          `yaml:"retry"`
	CircuitBreaker CircuitBreakerConfig `yaml:"circuit_breaker"`
}

type RetryConfig struct {
	MaxAttempts int `yaml:"max_attempts"`
	BaseDelayMS int `yaml:"base_delay_ms"`
	CapDelayMS  int `yaml:"cap_delay_ms"`
}

type CircuitBreakerConfig struct {
	FailureThreshold int `yaml:"failure_threshold"`
	RecoverySeconds  int `yaml:"recovery_seconds"`
}

func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	type alias Config
	aux := struct {
		alias  `yaml:",inline"`
		Models []ModelEntry `yaml:"models"`
	}{
		alias: alias(*c),
	}
	if err := node.Decode(&aux); err != nil {
		return err
	}
	*c = Config(aux.alias)
	c.Models = GetModels(aux.Models)
	return nil
}
func GetModels(entries []ModelEntry) map[string]*ModelConfig {
	if len(entries) == 0 {
		return map[string]*ModelConfig{
			defaultYAMLModel: {
				Name:           defaultYAMLModel,
				Provider:       "kimi",
				Model:          "moonshot-v1-8k",
				BaseURL:        "https://api.moonshot.cn/v1",
				APIKey:         os.Getenv("MOONSHOT_API_KEY"),
				TimeoutSeconds: 60,
			},
		}
	}

	out := make(map[string]*ModelConfig, len(entries))
	for _, m := range entries {
		modelCfg := ModelConfig{
			Name:           m.Name,
			Provider:       m.Provider,
			Model:          m.Model,
			TimeoutSeconds: m.TimeoutSeconds,
		}
		modelCfg.BaseURL = m.BaseURL
		if modelCfg.BaseURL == "" {
			modelCfg.BaseURL = m.APIBase
		}

		// API-key precedence: api_key_env → api_key:$VAR → literal.
		switch {
		case m.APIKeyEnv != "":
			modelCfg.APIKey = os.Getenv(m.APIKeyEnv)
		case strings.HasPrefix(m.APIKey, "$"):
			modelCfg.APIKey = os.Getenv(strings.TrimPrefix(m.APIKey, "$"))
		case strings.TrimSpace(m.APIKey) != "":
			modelCfg.APIKey = strings.TrimSpace(m.APIKey)
		}

		if modelCfg.Provider == "" {
			lower := strings.ToLower(modelCfg.BaseURL + m.Name)
			switch {
			case strings.Contains(lower, "moonshot") || strings.Contains(lower, "kimi"):
				modelCfg.Provider = "kimi"
			case strings.Contains(lower, "anthropic") || strings.Contains(lower, "claude"):
				modelCfg.Provider = "claude"
			case strings.Contains(lower, "openai"):
				modelCfg.Provider = "openai"
			default:
				modelCfg.Provider = "openai"
			}
		}
		out[m.Name] = &modelCfg
	}
	return out
}

func loadFromYAML(root string) (*Config, error) {

	data, err := os.ReadFile(filepath.Join(root, "yaml", "config.yaml"))
	if err != nil {
		return nil, fmt.Errorf("read yaml config: %w", err)
	}

	var config Config
	if err = yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parse yaml config: %w", err)
	}

	return &config, nil
}
