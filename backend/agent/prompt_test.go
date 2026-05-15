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

// AGENTS.md is multi-section. Only §Agent 工作纪律§ goes into the system
// prompt; the rest is read on demand. These tests lock that contract in.
const agentsMDFixture = `# AGENTS.md

## 核心原则

> Structs hold data only.

CODE_STYLE_BODY_MARKER

## Agent 工作纪律

DISCIPLINE_BODY_MARKER

### 1. 想清楚再下手

- be careful

## 何时不适用

trailing section
`

func TestLoadAgentsMDPrompt_ExtractsAgentDisciplineOnly(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"),
		[]byte(agentsMDFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadAgentsMDPrompt(&config.Config{RootDir: root})

	if !strings.HasPrefix(got, "<agent_discipline>\n") ||
		!strings.HasSuffix(got, "\n</agent_discipline>") {
		t.Fatalf("expected <agent_discipline> wrapper, got:\n%s", got)
	}
	if !strings.Contains(got, "DISCIPLINE_BODY_MARKER") {
		t.Fatalf("agent discipline body missing:\n%s", got)
	}
	if !strings.Contains(got, "### 1. 想清楚再下手") {
		t.Fatalf("nested ### subheadings should be preserved:\n%s", got)
	}
	if strings.Contains(got, "CODE_STYLE_BODY_MARKER") {
		t.Fatalf("§核心原则§ body must NOT leak into prompt:\n%s", got)
	}
	if strings.Contains(got, "trailing section") {
		t.Fatalf("§何时不适用§ body must NOT leak into prompt:\n%s", got)
	}
}

func TestLoadAgentsMDPrompt_MissingSectionReturnsEmpty(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"),
		[]byte("# AGENTS.md\n\n## 核心原则\n\nbody only\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadAgentsMDPrompt(&config.Config{RootDir: root}); got != "" {
		t.Fatalf("missing §Agent 工作纪律§ should produce empty, got %q", got)
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
// <agent_discipline> wrapper, but §核心原则§ stays out.
func TestGetSystemPrompt_AgentDisciplineInjected(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"),
		[]byte(agentsMDFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{RootDir: root}
	out := GetSystemPrompt("default", false, cfg)

	if !strings.Contains(out, "<agent_discipline>\nDISCIPLINE_BODY_MARKER") {
		t.Fatalf("expected <agent_discipline> wrapper opening with body:\n%s", out)
	}
	if strings.Contains(out, "CODE_STYLE_BODY_MARKER") {
		t.Fatalf("§核心原则§ body must NOT appear in system prompt:\n%s", out)
	}
}

// Critical_reminders mentions the literal string "<agent_discipline>"
// inline (in the "Code style on demand" rule), so detecting wrapper
// presence requires matching the precise "<agent_discipline>\n" opening
// rather than the bare tag.
func TestGetSystemPrompt_NoAgentsMDOmitsSection(t *testing.T) {
	cfg := &config.Config{RootDir: t.TempDir()}
	out := GetSystemPrompt("default", false, cfg)
	if strings.Contains(out, "<agent_discipline>\n") {
		t.Fatalf("missing AGENTS.md must not produce wrapper:\n%s", out)
	}
}

// extractTopLevelSection unit-tests the slicing edge cases in isolation
// from filesystem so failures point at the parser, not the I/O.
func TestExtractTopLevelSection(t *testing.T) {
	cases := []struct {
		name, text, title, want string
	}{
		{
			name:  "middle section trimmed at next ##",
			text:  "## A\n\naaa\n\n## B\n\nbbb\n\n## C\nccc\n",
			title: "B",
			want:  "bbb",
		},
		{
			name:  "last section runs to EOF",
			text:  "## A\n\naaa\n\n## Z\n\nzzz trailing\n",
			title: "Z",
			want:  "zzz trailing",
		},
		{
			name:  "missing returns empty",
			text:  "## A\n\naaa\n",
			title: "Nope",
			want:  "",
		},
		{
			name:  "preserves nested ### subheadings",
			text:  "## Top\n\nintro\n\n### Sub\n\nbody\n\n## Next\n",
			title: "Top",
			want:  "intro\n\n### Sub\n\nbody",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractTopLevelSection(tc.text, tc.title); got != tc.want {
				t.Fatalf("extractTopLevelSection(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}
