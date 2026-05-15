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

	cfg := &config.Config{
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
	if got := loadEnabledSkillsFromConfig(&config.Config{}); got != nil {
		t.Fatalf("empty config should yield nil skill list, got %+v", got)
	}
}

func TestDeferredToolNamesFromConfig_NilWhenEmpty(t *testing.T) {
	if got := DeferredToolNamesFromConfig(&config.Config{}); got != nil {
		t.Fatal("expected nil slice when no deferred tools configured")
	}
	cfg := &config.Config{
		ToolSearch: config.ToolSearchConfig{
			Deferred: []config.DeferredToolEntry{{Name: "fancy_search"}},
		},
	}
	got := DeferredToolNamesFromConfig(cfg)
	if len(got) != 1 || got[0] != "fancy_search" {
		t.Fatalf("DeferredToolNamesFromConfig mismatch: %+v", got)
	}
}

func TestGetSystemPrompt_SkillsAndDeferredSectionsRendered(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "demo"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "demo", "SKILL.md"),
		[]byte("---\nname: demo\ndescription: Demo skill.\n---\n"), 0o644)

	cfg := &config.Config{
		Skills: config.SkillsConfig{Paths: []string{root}},
		ToolSearch: config.ToolSearchConfig{
			Enabled: true,
			Deferred: []config.DeferredToolEntry{
				{Name: "fancy_search"},
			},
		},
	}

	out := GetSystemPrompt("default", false, cfg)

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

func TestGetSystemPrompt_EmptyMemorySkipsBlock(t *testing.T) {
	cfg := &config.Config{
		RootDir: t.TempDir(),
		Memory:  config.Memory{Enabled: true, InjectionEnabled: true, MaxInjectionTokens: 1024},
	}
	out := GetSystemPrompt("default", false, cfg)
	if strings.Contains(out, "<memory>") {
		t.Fatalf("empty store should skip <memory> section, got:\n%s", out)
	}
}

func TestLoadSoulPromptWrapsMarkdown(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "yaml", "soul.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("**Identity**\nAlice\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := loadSoulPrompt(&config.Config{RootDir: root})
	want := "<soul>\n**Identity**\nAlice\n</soul>"
	if got != want {
		t.Fatalf("loadSoulPrompt() = %q, want %q", got, want)
	}
}

func TestLoadSoulPromptMissingFile(t *testing.T) {
	if got := loadSoulPrompt(&config.Config{RootDir: t.TempDir()}); got != "" {
		t.Fatalf("missing soul should be empty, got %q", got)
	}
}

// loadAgentsMDPrompt mirrors loadSoulPrompt in shape; same nil / missing
// / present matrix locks the parity in.
func TestLoadAgentsMDPromptWrapsContent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"),
		[]byte("hello rules\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadAgentsMDPrompt(&config.Config{RootDir: root})
	want := "<workspace_conventions>\nhello rules\n</workspace_conventions>"
	if got != want {
		t.Fatalf("loadAgentsMDPrompt() = %q, want %q", got, want)
	}
}

func TestLoadAgentsMDPromptMissingFile(t *testing.T) {
	if got := loadAgentsMDPrompt(&config.Config{RootDir: t.TempDir()}); got != "" {
		t.Fatalf("missing AGENTS.md should be empty, got %q", got)
	}
}

func TestLoadAgentsMDPromptNilCfg(t *testing.T) {
	if got := loadAgentsMDPrompt(nil); got != "" {
		t.Fatalf("nil cfg must be empty (defensive zero-value), got %q", got)
	}
}

// AGENTS.md present in cfg.RootDir → system prompt embeds the
// <workspace_conventions> wrapper. Missing → no wrapper anywhere
// (template collapses {agents_md} → "").
func TestGetSystemPrompt_AgentsMDInjected(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"),
		[]byte("never use sed; prefer StrReplace\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{RootDir: root}
	out := GetSystemPrompt("default", false, cfg)

	if !strings.Contains(out, "<workspace_conventions>") {
		t.Fatalf("expected <workspace_conventions> in prompt:\n%s", out)
	}
	if !strings.Contains(out, "never use sed; prefer StrReplace") {
		t.Fatalf("AGENTS.md content missing from prompt:\n%s", out)
	}
}

func TestGetSystemPrompt_NoAgentsMDOmitsSection(t *testing.T) {
	cfg := &config.Config{RootDir: t.TempDir()}
	out := GetSystemPrompt("default", false, cfg)
	if strings.Contains(out, "<workspace_conventions>") {
		t.Fatalf("missing AGENTS.md must not produce wrapper:\n%s", out)
	}
}
