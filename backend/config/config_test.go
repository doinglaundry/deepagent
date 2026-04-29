package config

import "testing"


func TestNormalizeConfigKeepsExplicitNewFields(t *testing.T) {
	cfg, err := normalizeConfig(Config{
		DefaultModel: "primary",
		Models: map[string]*ModelConfig{
			"primary": {
				Name:           "primary",
				Provider:       "openai",
				Model:          "gpt-4o",
				BaseURL:        "https://proxy.example.com",
				APIKeyEnv:      "OPENAI_API_KEY",
				TimeoutSeconds: 20,
			},
		},
		DefaultAgent: "planner",
		Agents: map[string]AgentConfig{
			"planner": {
				Name:         "planner-agent",
				Instruction:  "Plan before coding",
				MaxIteration: 9,
			},
		},
		RuntimeTimeout: 20,
	})
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}

	modelCfg := cfg.Models["primary"]
	if modelCfg.Provider != "openai" || modelCfg.Model != "gpt-4o" {
		t.Fatalf("explicit model config should be preserved: %+v", modelCfg)
	}
	if modelCfg.APIKeyEnv != "OPENAI_API_KEY" {
		t.Fatalf("unexpected api key env: %q", modelCfg.APIKeyEnv)
	}

	agentCfg := cfg.Agents["planner"]
	if agentCfg.Name != "planner-agent" || agentCfg.MaxIteration != 9 {
		t.Fatalf("explicit agent config should be preserved: %+v", agentCfg)
	}
}

func TestDefaultAPIKeyEnv(t *testing.T) {
	if got := defaultAPIKeyEnv("openai"); got != "OPENAI_API_KEY" {
		t.Fatalf("unexpected openai key env: %q", got)
	}
	if got := defaultAPIKeyEnv("claude"); got != "ANTHROPIC_API_KEY" {
		t.Fatalf("unexpected claude key env: %q", got)
	}
}
