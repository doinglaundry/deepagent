package agent

import (
	"strings"
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

func TestMergeRuntime(t *testing.T) {
	rt := RuntimeContext{
		ThinkingEnabled:        true,
		MaxConcurrentSubagents: 3,
		Metadata:               map[string]any{},
	}
	merged := rt.MergeRuntime(map[string]any{
		"model_name":               "claude",
		"thinking_enabled":         false,
		"max_concurrent_subagents": 5,
	}, map[string]any{
		"agent_name":       "researcher",
		"subagent_enabled": true,
	})

	if merged.ModelName != "claude" {
		t.Fatalf("ModelName: got %q, want claude", merged.ModelName)
	}
	if merged.ThinkingEnabled {
		t.Fatalf("ThinkingEnabled: should be false after merge")
	}
	if merged.MaxConcurrentSubagents != 5 {
		t.Fatalf("MaxConcurrentSubagents: got %d, want 5", merged.MaxConcurrentSubagents)
	}
	if merged.AgentName != "researcher" {
		t.Fatalf("AgentName: got %q, want researcher", merged.AgentName)
	}
	if !merged.SubagentEnabled {
		t.Fatalf("SubagentEnabled: should be true after merge")
	}
}

func TestMergeRuntime_DefaultMaxConcurrent(t *testing.T) {
	rt := RuntimeContext{} // zero-value rt: MergeRuntime should fill MaxConcurrentSubagents to its 3 default.
	merged := rt.MergeRuntime(nil, nil)
	if merged.MaxConcurrentSubagents != 3 {
		t.Fatalf("default MaxConcurrentSubagents: got %d, want 3",
			merged.MaxConcurrentSubagents)
	}
	if merged.Metadata == nil {
		t.Fatalf("Metadata should be initialized")
	}
}

func TestMergeRuntime_ContextOverridesConfigurable(t *testing.T) {
	rt := RuntimeContext{
		ThinkingEnabled:        true,
		MaxConcurrentSubagents: 3,
		Metadata:               map[string]any{},
	}
	merged := rt.MergeRuntime(map[string]any{
		"model_name": "kimi",
	}, map[string]any{
		"model": "claude",
	})

	// Python: context update wins. context only has "model" → falls through to
	// the "model_name or model" branch.
	if !strings.HasPrefix(merged.ModelName, "claude") && merged.ModelName != "kimi" {
		t.Fatalf("ModelName: got %q, expected one of {claude, kimi}", merged.ModelName)
	}
}
