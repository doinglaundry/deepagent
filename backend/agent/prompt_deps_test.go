package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/backend/config"
)

func TestLoadEnabledSkillsFromConfig_FromPaths(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "demo"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "demo", "SKILL.md"),
		[]byte("---\nname: demo\ndescription: A demo skill.\n---\n"), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}

	cfg := config.Config{
		Skills: config.SkillsConfig{Paths: []string{root}},
	}
	got := loadEnabledSkillsFromConfig(cfg)
	if len(got) != 1 {
		t.Fatalf("loadEnabledSkillsFromConfig: got %d, want 1: %+v", len(got), got)
	}
	if got[0].Name != "demo" || got[0].Description != "A demo skill." {
		t.Fatalf("loaded skill mismatch: %+v", got[0])
	}
}

func TestLoadEnabledSkillsFromConfig_NoPaths(t *testing.T) {
	if got := loadEnabledSkillsFromConfig(config.Config{}); got != nil {
		t.Fatalf("empty config should yield nil skill list, got %+v", got)
	}
}

func TestGetDeferredToolNames(t *testing.T) {
	cfg := config.Config{
		ToolSearch: config.ToolSearchConfig{
			Enabled: true,
			Deferred: []config.DeferredToolEntry{
				{Name: "web_search", Description: "web search"},
				{Name: "shell", Description: "shell"},
			},
		},
	}
	names := getDeferredToolNames(cfg)
	if len(names) != 2 || names[0] != "web_search" || names[1] != "shell" {
		t.Fatalf("getDeferredToolNames mismatch: %+v", names)
	}

	if got := getDeferredToolNames(config.Config{}); got != nil {
		t.Fatalf("empty cfg should yield nil, got %+v", got)
	}
}

func TestDeferredToolNamesFromConfig_ClosureNilWhenEmpty(t *testing.T) {
	if fn := DeferredToolNamesFromConfig(config.Config{}); fn != nil {
		t.Fatal("expected nil closure when no deferred tools configured")
	}
	cfg := config.Config{
		ToolSearch: config.ToolSearchConfig{
			Deferred: []config.DeferredToolEntry{{Name: "fancy_search"}},
		},
	}
	fn := DeferredToolNamesFromConfig(cfg)
	if fn == nil {
		t.Fatal("expected non-nil closure")
	}
	got := fn()
	if len(got) != 1 || got[0] != "fancy_search" {
		t.Fatalf("closure result mismatch: %+v", got)
	}
}

func TestApplyPromptTemplate_SkillsAndDeferredSectionsRendered(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "demo"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "demo", "SKILL.md"),
		[]byte("---\nname: demo\ndescription: Demo skill.\n---\n"), 0o644)

	cfg := config.Config{
		Skills: config.SkillsConfig{Paths: []string{root}},
		ToolSearch: config.ToolSearchConfig{
			Enabled: true,
			Deferred: []config.DeferredToolEntry{
				{Name: "fancy_search"},
			},
		},
	}

	out := ApplyPromptTemplate(PromptOptions{
		AgentName: "default",
		Config:    cfg,
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

func TestApplyPromptTemplate_NilMemSkipsMemorySection(t *testing.T) {
	cfg := config.Config{
		Memory: config.Memory{Enabled: true, InjectionEnabled: true, MaxInjectionTokens: 1024},
	}
	out := ApplyPromptTemplate(PromptOptions{
		AgentName: "default",
		Config:    cfg,
		Mem:       nil,
	})
	if strings.Contains(out, "<memory>") {
		t.Fatalf("nil Mem should skip <memory> section, got:\n%s", out)
	}
}
