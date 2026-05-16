package config

import (
	"strings"
	"testing"
)

// validateModelConfig must catch empty API key — that is the exact
// regression that lets an empty Authorization header through and
// produces a misleading 401 at the first chat completion.
func TestValidateModelConfig(t *testing.T) {
	good := &ModelConfig{
		Name:     "ark-deepseek",
		Provider: "openai",
		Model:    "deepseek-v3-2-251201",
		BaseURL:  "https://ark.cn-beijing.volces.com/api/v3",
		APIKey:   "sk-real-key",
	}

	t.Run("ok", func(t *testing.T) {
		if err := validateModelConfig(good); err != nil {
			t.Fatalf("good config rejected: %v", err)
		}
	})

	type tc struct {
		name    string
		mutate  func(*ModelConfig)
		wantSub string
	}
	cases := []tc{
		{"nil", func(_ *ModelConfig) {}, "nil"},
		{"empty_name", func(m *ModelConfig) { m.Name = "" }, "name"},
		{"empty_provider", func(m *ModelConfig) { m.Provider = "" }, "provider"},
		{"empty_model", func(m *ModelConfig) { m.Model = "" }, "model"},
		{"empty_base_url", func(m *ModelConfig) { m.BaseURL = "" }, "base_url"},
		{"empty_api_key", func(m *ModelConfig) { m.APIKey = "" }, "api_key"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var m *ModelConfig
			if c.name != "nil" {
				cp := *good
				c.mutate(&cp)
				m = &cp
			}
			err := validateModelConfig(m)
			if err == nil {
				t.Fatalf("%s: want error, got nil", c.name)
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Fatalf("%s: error %q missing %q", c.name, err.Error(), c.wantSub)
			}
		})
	}
}

// CompleteDefaultModelConfig must error loud at startup when the
// resolved api_key is empty — that is the actual user-facing fix.
// Using api_key_env that points at an unset env var is the canonical
// way to land here.
func TestCompleteDefaultModelConfig(t *testing.T) {
	t.Run("ok", func(t *testing.T) {
		cfg := &Config{
			DefaultModel: "ark-deepseek",
			Models: map[string]*ModelConfig{
				"ark-deepseek": {
					Name:     "ark-deepseek",
					Provider: "openai",
					Model:    "deepseek-v3-2-251201",
					BaseURL:  "https://ark.cn-beijing.volces.com/api/v3",
					APIKey:   "sk-real-key",
				},
			},
		}
		if err := CompleteDefaultModelConfig(cfg); err != nil {
			t.Fatalf("good config rejected: %v", err)
		}
	})

	t.Run("empty_default_model", func(t *testing.T) {
		err := CompleteDefaultModelConfig(&Config{DefaultModel: ""})
		if err == nil || !strings.Contains(err.Error(), "default_model") {
			t.Fatalf("want default_model error, got %v", err)
		}
	})

	t.Run("default_model_not_found", func(t *testing.T) {
		err := CompleteDefaultModelConfig(&Config{
			DefaultModel: "missing",
			Models:       map[string]*ModelConfig{},
		})
		if err == nil || !strings.Contains(err.Error(), "not found") {
			t.Fatalf("want not-found error, got %v", err)
		}
	})

	t.Run("empty_api_key_surfaces_at_startup", func(t *testing.T) {
		cfg := &Config{
			DefaultModel: "ark-deepseek",
			Models: map[string]*ModelConfig{
				"ark-deepseek": {
					Name:     "ark-deepseek",
					Provider: "openai",
					Model:    "deepseek-v3-2-251201",
					BaseURL:  "https://ark.cn-beijing.volces.com/api/v3",
					APIKey:   "",
				},
			},
		}
		err := CompleteDefaultModelConfig(cfg)
		if err == nil {
			t.Fatal("empty api_key should fail validation, got nil")
		}
		if !strings.Contains(err.Error(), "api_key") {
			t.Fatalf("error must mention api_key so user can fix: %v", err)
		}
		if !strings.Contains(err.Error(), "ark-deepseek") {
			t.Fatalf("error must mention which model: %v", err)
		}
	})
}
