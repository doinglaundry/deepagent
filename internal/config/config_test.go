package config

import "testing"

func TestNormalizeConfigFromLegacyRuntimeFields(t *testing.T) {
	cfg, err := normalizeConfig(Config{
		RuntimeModel:   "claude-sonnet-4-6",
		RuntimeBaseURL: "https://api.example.com",
		RuntimeTimeout: 45,
	})
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}

	if cfg.DefaultModel != "claude-sonnet-4-6" {
		t.Fatalf("unexpected default model: %q", cfg.DefaultModel)
	}
	modelCfg, ok := cfg.Models[cfg.DefaultModel]
	if !ok {
		t.Fatalf("default model config not found: %q", cfg.DefaultModel)
	}
	if modelCfg.Provider != "claude" {
		t.Fatalf("unexpected provider: %q", modelCfg.Provider)
	}
	if modelCfg.Model != "claude-sonnet-4-6" {
		t.Fatalf("unexpected model: %q", modelCfg.Model)
	}
	if modelCfg.APIKeyEnv != "ANTHROPIC_API_KEY" {
		t.Fatalf("unexpected api key env: %q", modelCfg.APIKeyEnv)
	}
	if modelCfg.TimeoutSeconds != 45 {
		t.Fatalf("unexpected timeout: %d", modelCfg.TimeoutSeconds)
	}

	if cfg.DefaultAgent != defaultAgentKey {
		t.Fatalf("unexpected default agent: %q", cfg.DefaultAgent)
	}
	agentCfg, ok := cfg.Agents[cfg.DefaultAgent]
	if !ok {
		t.Fatalf("default agent config not found: %q", cfg.DefaultAgent)
	}
	if agentCfg.Name != defaultAgentName {
		t.Fatalf("unexpected agent name: %q", agentCfg.Name)
	}
	if agentCfg.MaxIteration != defaultAgentIterations {
		t.Fatalf("unexpected max iteration: %d", agentCfg.MaxIteration)
	}
}

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
