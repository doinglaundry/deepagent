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

func TestDeferredToolNamesFromConfig_NilWhenEmpty(t *testing.T) {
	if got := DeferredToolNamesFromConfig(config.Config{}); got != nil {
		t.Fatal("expected nil slice when no deferred tools configured")
	}
	cfg := config.Config{
		ToolSearch: config.ToolSearchConfig{
			Deferred: []config.DeferredToolEntry{{Name: "fancy_search"}},
		},
	}
	got := DeferredToolNamesFromConfig(cfg)
	if len(got) != 1 || got[0] != "fancy_search" {
		t.Fatalf("DeferredToolNamesFromConfig mismatch: %+v", got)
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

	out := ApplyPromptTemplate(RuntimeContext{AgentName: "default"}, nil, cfg, nil)

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
	out := ApplyPromptTemplate(RuntimeContext{AgentName: "default"}, nil, cfg, nil)
	if strings.Contains(out, "<memory>") {
		t.Fatalf("nil Mem should skip <memory> section, got:\n%s", out)
	}
}
