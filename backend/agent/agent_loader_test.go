package agent

import (
	"testing"

	"eino-cli/backend/config"
)

// TestGetAgentConfig_HappyPath checks the inline-map path: a name that
// exists in cfg.Agents resolves with all fields preserved.
func TestGetAgentConfig_HappyPath(t *testing.T) {
	cfg := config.Config{
		Agents: map[string]config.AgentConfig{
			"writer": {
				Name:       "writer",
				Model:      "kimi",
				ToolGroups: []string{"filesystem"},
			},
		},
	}
	agentConfig, err := GetAgentConfig(cfg, "writer")
	if err != nil || agentConfig == nil {
		t.Fatalf("expected inline lookup, got (%+v, %v)", agentConfig, err)
	}
	if agentConfig.Model != "kimi" {
		t.Errorf("Model = %q", agentConfig.Model)
	}
}

// TestGetAgentConfig_NameFallsBackToKey verifies the empty-Name field
// in cfg.Agents is filled from the requested key.
func TestGetAgentConfig_NameFallsBackToKey(t *testing.T) {
	cfg := config.Config{
		Agents: map[string]config.AgentConfig{
			"writer": {Model: "kimi"},
		},
	}
	agentConfig, err := GetAgentConfig(cfg, "writer")
	if err != nil || agentConfig == nil {
		t.Fatalf("expected inline lookup, got (%+v, %v)", agentConfig, err)
	}
	if agentConfig.Name != "writer" {
		t.Errorf("Name = %q, want writer", agentConfig.Name)
	}
}

// TestGetAgentConfig_EmptyNameIsSoftMiss preserves the "no specific
// agent requested → use defaults" signal for empty input. Distinct
// from the strict missing-name path below.
func TestGetAgentConfig_EmptyNameIsSoftMiss(t *testing.T) {
	cfg := config.Config{Agents: map[string]config.AgentConfig{}}
	agentConfig, err := GetAgentConfig(cfg, "")
	if err != nil {
		t.Fatalf("expected no error for empty name, got %v", err)
	}
	if agentConfig != nil {
		t.Errorf("expected nil profile for empty name, got %+v", agentConfig)
	}
}

// TestGetAgentConfig_RaisesOnMissingAgent locks in the strict
// behaviour: a non-empty name not found in cfg.Agents propagates an
// error rather than silently degrading to a default profile. A soft
// miss would let typos flow through to a wrong-but-plausible default
// agent without any signal.
func TestGetAgentConfig_RaisesOnMissingAgent(t *testing.T) {
	cfg := config.Config{Agents: map[string]config.AgentConfig{}}
	agentConfig, err := GetAgentConfig(cfg, "ghost")
	if err == nil {
		t.Fatalf("expected error for missing agent, got profile=%+v", agentConfig)
	}
	if agentConfig != nil {
		t.Errorf("expected nil profile alongside error, got %+v", agentConfig)
	}
}

// TestGetAgentConfig_RejectsInvalidName ensures the validation hook
// runs before any lookup. Mirrors Python ValueError on bad chars.
func TestGetAgentConfig_RejectsInvalidName(t *testing.T) {
	cfg := config.Config{Agents: map[string]config.AgentConfig{}}
	_, err := GetAgentConfig(cfg, "../etc/passwd")
	if err == nil {
		t.Fatal("expected error for invalid agent name")
	}
}
