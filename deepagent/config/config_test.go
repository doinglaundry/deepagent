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
