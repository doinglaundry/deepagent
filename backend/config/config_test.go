package config

import (
	"os"
	"path/filepath"
	"testing"
)


func TestNormalizeConfigKeepsExplicitFields(t *testing.T) {
	cfg, err := normalizeConfig(Config{
		DefaultModel: "primary",
		Models: map[string]*ModelConfig{
			"primary": {
				Name:           "primary",
				Provider:       "openai",
				Model:          "gpt-4o",
				BaseURL:        "https://proxy.example.com",
				APIKey:         "sk-explicit",
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
	})
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}

	modelCfg := cfg.Models["primary"]
	if modelCfg.Provider != "openai" || modelCfg.Model != "gpt-4o" {
		t.Fatalf("explicit model config should be preserved: %+v", modelCfg)
	}
	if modelCfg.APIKey != "sk-explicit" {
		t.Fatalf("explicit literal api key should pass through unchanged, got %q", modelCfg.APIKey)
	}

	agentCfg := cfg.Agents["planner"]
	if agentCfg.Name != "planner-agent" || agentCfg.MaxIteration != 9 {
		t.Fatalf("explicit agent config should be preserved: %+v", agentCfg)
	}
}


func TestAppendDefaultSkillsPath(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "backend", "skills")

	t.Run("missing dir is a no-op", func(t *testing.T) {
		got := appendDefaultSkillsPath(root, SkillsConfig{})
		if len(got.Paths) != 0 {
			t.Fatalf("expected no paths when backend/skills is missing, got %v", got.Paths)
		}
	})

	if err := os.MkdirAll(want, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	t.Run("appends when present", func(t *testing.T) {
		got := appendDefaultSkillsPath(root, SkillsConfig{})
		if len(got.Paths) != 1 || got.Paths[0] != want {
			t.Fatalf("paths: got %v, want [%q]", got.Paths, want)
		}
	})

	t.Run("idempotent when already configured", func(t *testing.T) {
		skillsCfg := SkillsConfig{Paths: []string{want, "/other/path"}}
		got := appendDefaultSkillsPath(root, skillsCfg)
		if len(got.Paths) != 2 {
			t.Fatalf("expected 2 paths preserved, got %v", got.Paths)
		}
	})

	t.Run("empty root short-circuits", func(t *testing.T) {
		got := appendDefaultSkillsPath("", SkillsConfig{Paths: []string{"/x"}})
		if len(got.Paths) != 1 || got.Paths[0] != "/x" {
			t.Fatalf("empty root must not mutate input: got %v", got.Paths)
		}
	})
}

func TestDefaultAPIKeyEnv(t *testing.T) {
	if got := defaultAPIKeyEnv("openai"); got != "OPENAI_API_KEY" {
		t.Fatalf("unexpected openai key env: %q", got)
	}
	if got := defaultAPIKeyEnv("claude"); got != "ANTHROPIC_API_KEY" {
		t.Fatalf("unexpected claude key env: %q", got)
	}
}

// When yaml supplies neither api_key nor api_key_env, normalizeConfig
// falls back to the provider's canonical env via defaultAPIKeyEnv.
// The yaml loader resolves env-var indirection at decode time, so by
// the time normalizeConfig runs, an empty mc.APIKey means "nobody
// supplied a key anywhere" and the provider default takes over.
func TestNormalizeConfigFallsBackToProviderEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-from-env")

	cfg, err := normalizeConfig(Config{
		DefaultModel: "primary",
		Models: map[string]*ModelConfig{
			"primary": {
				Name:           "primary",
				Provider:       "openai",
				Model:          "gpt-4o",
				TimeoutSeconds: 20,
			},
		},
		DefaultAgent: "planner",
		Agents: map[string]AgentConfig{
			"planner": {Name: "planner-agent", Instruction: "Plan first", MaxIteration: 1},
		},
	})
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
	if got := cfg.Models["primary"].APIKey; got != "sk-from-env" {
		t.Fatalf("expected APIKey to be filled from OPENAI_API_KEY, got %q", got)
	}
}
