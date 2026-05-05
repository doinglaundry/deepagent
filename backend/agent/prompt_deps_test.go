package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/backend/config"
	"eino-cli/backend/config/schema"
)

func TestBuildPromptDeps_SkillsWiredFromConfigPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "demo"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "demo", "SKILL.md"),
		[]byte("---\nname: demo\ndescription: A demo skill.\n---\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	cfg := config.Config{
		Skills: schema.SkillsConfig{Paths: []string{root}},
	}
	deps := BuildPromptDeps(cfg, PromptDepsOptions{})
	if deps.LoadSkills == nil {
		t.Fatal("LoadSkills should be wired")
	}
	got := deps.LoadSkills()
	if len(got) != 1 {
		t.Fatalf("LoadSkills: got %d, want 1: %+v", len(got), got)
	}
	if got[0].Name != "demo" || got[0].Description != "A demo skill." {
		t.Fatalf("loaded skill mismatch: %+v", got[0])
	}

	// Cached: a second call should not rescan disk and should return the
	// same slice.
	cached := deps.LoadSkills()
	if len(cached) != 1 || cached[0].Name != "demo" {
		t.Fatalf("cached LoadSkills mismatch: %+v", cached)
	}
}

func TestBuildPromptDeps_DeferredAndACPWired(t *testing.T) {
	cfg := config.Config{
		ToolSearch: schema.ToolSearchConfig{
			Enabled: true,
			Deferred: []schema.DeferredToolEntry{
				{Name: "web_search", Description: "web search"},
				{Name: "shell", Description: "shell"},
			},
		},
		ACP: schema.ACPConfig{
			Agents: map[string]schema.ACPAgentEntry{
				"codex": {Description: "codex agent"},
			},
		},
	}
	deps := BuildPromptDeps(cfg, PromptDepsOptions{})
	if deps.GetDeferredRegistry == nil {
		t.Fatal("GetDeferredRegistry should be wired")
	}
	reg := deps.GetDeferredRegistry()
	if len(reg) != 2 || reg[0].Name != "web_search" {
		t.Fatalf("registry mismatch: %+v", reg)
	}

	if deps.GetACPAgents == nil {
		t.Fatal("GetACPAgents should be wired")
	}
	acp := deps.GetACPAgents()
	if _, ok := acp["codex"]; !ok {
		t.Fatalf("codex missing from ACP map: %+v", acp)
	}
}

func TestBuildPromptDeps_EmptyConfigDegradesGracefully(t *testing.T) {
	deps := BuildPromptDeps(config.Config{}, PromptDepsOptions{})
	if deps.LoadSkills == nil {
		t.Fatal("LoadSkills should always be set")
	}
	if got := deps.LoadSkills(); got != nil {
		t.Fatalf("empty config should yield nil skill list, got %+v", got)
	}
	if deps.GetDeferredRegistry != nil {
		t.Fatal("GetDeferredRegistry should remain nil when no deferred tools configured")
	}
	if deps.GetACPAgents != nil {
		t.Fatal("GetACPAgents should remain nil when no agents configured")
	}
}

func TestApplyPromptTemplate_SkillsAndDeferredSectionsRendered(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "demo"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "demo", "SKILL.md"),
		[]byte("---\nname: demo\ndescription: Demo skill.\n---\n"), 0o644)

	cfg := config.Config{
		Skills: schema.SkillsConfig{Paths: []string{root}},
		ToolSearch: schema.ToolSearchConfig{
			Enabled: true,
			Deferred: []schema.DeferredToolEntry{
				{Name: "fancy_search"},
			},
		},
	}
	deps := BuildPromptDeps(cfg, PromptDepsOptions{})
	app := BuildAppConfig(cfg)

	out := ApplyPromptTemplate(PromptOptions{
		AgentName: "default",
		AppConfig: app,
		Deps:      deps,
	})

	if !strings.Contains(out, "<available_skills>") {
		t.Fatalf("available_skills section missing from prompt:\n%s", out)
	}
	if !strings.Contains(out, "<name>demo</name>") {
		t.Fatalf("demo skill not rendered:\n%s", out)
	}
	if !strings.Contains(out, "<available-deferred-tools>") || !strings.Contains(out, "fancy_search") {
		t.Fatalf("deferred-tools section missing from prompt")
	}
}
