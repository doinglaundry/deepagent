package agent

import (
	"os"
	"path/filepath"
	"testing"

	"eino-cli/backend/config"
)

const sampleAgentYAML = `name: researcher
description: Deep research specialist.
model: claude-3.5
tool_groups:
  - filesystem
  - shell
skills:
  - lark-base
instruction: |
  You are a research specialist.
max_iteration: 12
`

// TestLoadAgentConfigFromDir_HappyPath writes a minimal
// agents/<name>/config.yaml fixture and asserts every field round-trips
// onto config.AgentConfig in the order Python's load_agent_config defines.
func TestLoadAgentConfigFromDir_HappyPath(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "researcher")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(sampleAgentYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	profile, err := LoadAgentConfigFromDir(tmp, "researcher")
	if err != nil {
		t.Fatalf("LoadAgentConfigFromDir: %v", err)
	}
	if profile == nil {
		t.Fatal("expected non-nil profile")
	}
	if profile.Name != "researcher" {
		t.Errorf("Name = %q, want researcher", profile.Name)
	}
	if profile.Model != "claude-3.5" {
		t.Errorf("Model = %q", profile.Model)
	}
	if len(profile.ToolGroups) != 2 || profile.ToolGroups[0] != "filesystem" {
		t.Errorf("ToolGroups = %v", profile.ToolGroups)
	}
	if len(profile.Skills) != 1 || profile.Skills[0] != "lark-base" {
		t.Errorf("Skills = %v", profile.Skills)
	}
	if profile.Instruction == "" {
		t.Error("Instruction should be set from YAML")
	}
}

// TestLoadAgentConfigFromDir_NameLowerCased verifies the directory lookup
// matches Python's name.lower() rule so callers can pass mixed-case names.
func TestLoadAgentConfigFromDir_NameLowerCased(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "researcher")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("name: researcher\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	profile, err := LoadAgentConfigFromDir(tmp, "Researcher")
	if err != nil {
		t.Fatalf("expected lowercase lookup to succeed, got %v", err)
	}
	if profile == nil || profile.Name != "researcher" {
		t.Errorf("unexpected profile: %+v", profile)
	}
}

// TestLoadAgentConfigFromDir_MissingDir mirrors Python's
// FileNotFoundError when the agent dir doesn't exist.
func TestLoadAgentConfigFromDir_MissingDir(t *testing.T) {
	tmp := t.TempDir()
	_, err := LoadAgentConfigFromDir(tmp, "ghost")
	if err == nil {
		t.Fatal("expected error for missing agent dir")
	}
}

// TestLoadAgentConfigFromDir_EmptyName mirrors Python returning None for
// the default agent (no custom config requested).
func TestLoadAgentConfigFromDir_EmptyName(t *testing.T) {
	profile, err := LoadAgentConfigFromDir("/tmp", "")
	if err != nil || profile != nil {
		t.Fatalf("expected (nil, nil), got (%+v, %v)", profile, err)
	}
}

// TestLoadAgentConfigFromConfig_PrefersInlineEntry checks the inline-map
// path is honoured before falling back to disk.
func TestLoadAgentConfigFromConfig_PrefersInlineEntry(t *testing.T) {
	cfg := config.Config{
		Agents: map[string]config.AgentConfig{
			"writer": {
				Name:       "writer",
				Model:      "kimi",
				ToolGroups: []string{"filesystem"},
			},
		},
	}
	profile, err := LoadAgentConfigFromConfig(cfg, "writer")
	if err != nil || profile == nil {
		t.Fatalf("expected inline lookup, got (%+v, %v)", profile, err)
	}
	if profile.Model != "kimi" {
		t.Errorf("Model = %q", profile.Model)
	}
}

// TestLoadAgentProfile_FallsThroughToDisk validates the high-level
// resolver: inline miss → disk lookup → success.
func TestLoadAgentProfile_FallsThroughToDisk(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "researcher")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(sampleAgentYAML), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg := config.Config{
		AgentsDir: tmp,
		Agents:    map[string]config.AgentConfig{},
	}
	profile, err := LoadAgentProfile(cfg, "researcher")
	if err != nil || profile == nil {
		t.Fatalf("expected disk fallback to succeed: profile=%+v err=%v", profile, err)
	}
	if profile.Model != "claude-3.5" {
		t.Errorf("Model = %q", profile.Model)
	}
}

// TestLoadAgentProfile_NoCustomProfileIsSoftMiss confirms the loader
// returns (nil, nil) instead of an error when neither inline nor disk
// has the agent. This matches Python's "default_agent" fallback path.
func TestLoadAgentProfile_NoCustomProfileIsSoftMiss(t *testing.T) {
	cfg := config.Config{AgentsDir: t.TempDir()}
	profile, err := LoadAgentProfile(cfg, "ghost")
	if err != nil {
		t.Fatalf("expected soft miss, got err: %v", err)
	}
	if profile != nil {
		t.Errorf("expected nil profile, got %+v", profile)
	}
}

// TestLoadAgentProfile_RejectsInvalidName ensures the validation hook
// runs before any disk access. Mirrors Python ValueError on bad chars.
func TestLoadAgentProfile_RejectsInvalidName(t *testing.T) {
	cfg := config.Config{AgentsDir: t.TempDir()}
	_, err := LoadAgentProfile(cfg, "../etc/passwd")
	if err == nil {
		t.Fatal("expected error for invalid agent name")
	}
}
