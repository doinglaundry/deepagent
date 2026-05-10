package agent

import (
	"testing"

	"eino-cli/backend/config"
)

func TestValidateAgentName(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "", false},
		{"   ", "", false},
		{"default", "default", false},
		{"my-agent_42", "my-agent_42", false},
		{"bad name", "", true},
		{"bad/name", "", true},
		{"中文", "", true},
	}
	for _, tc := range cases {
		got, err := ValidateAgentName(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ValidateAgentName(%q): expected error, got %q", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ValidateAgentName(%q): unexpected error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ValidateAgentName(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestGetModelName(t *testing.T) {
	cfg := config.Config{
		DefaultModel: "kimi",
		Models: map[string]*config.ModelConfig{
			"kimi":   {Name: "kimi", Provider: "kimi", Model: "moonshot-v1-8k"},
			"claude": {Name: "claude", Provider: "claude", Model: "claude-sonnet-4-6"},
		},
	}

	got, err := GetModelName("claude", cfg)
	if err != nil || got != "claude" {
		t.Fatalf("explicit model: got %q err=%v, want %q nil", got, err, "claude")
	}

	// Unknown name → fall back to default with warning, not error.
	got, err = GetModelName("unknown", cfg)
	if err != nil || got != "kimi" {
		t.Fatalf("fallback: got %q err=%v, want %q nil", got, err, "kimi")
	}

	// Empty name → default.
	got, err = GetModelName("", cfg)
	if err != nil || got != "kimi" {
		t.Fatalf("empty: got %q err=%v, want %q nil", got, err, "kimi")
	}

	// Empty config → error.
	if _, err := GetModelName("kimi", config.Config{}); err == nil {
		t.Fatalf("expected error on empty config")
	}
}

func TestGetModelConfig_PreferAgentConfig(t *testing.T) {
	cfg := config.Config{
		DefaultModel: "kimi",
		Models: map[string]*config.ModelConfig{
			"kimi":   {Name: "kimi", Provider: "kimi", Model: "moonshot-v1-8k"},
			"claude": {Name: "claude", Provider: "claude", Model: "claude-sonnet-4-6"},
		},
	}
	agentConfig := &config.AgentConfig{Model: "claude"}

	name, modelCfg, err := GetModelConfig("", agentConfig, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "claude" || modelCfg == nil || modelCfg.Name != "claude" {
		t.Fatalf("got name=%q modelCfg=%v, want claude", name, modelCfg)
	}

	// Explicit request beats agent config.
	name, _, err = GetModelConfig("kimi", agentConfig, cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if name != "kimi" {
		t.Fatalf("explicit: got %q, want kimi", name)
	}
}
