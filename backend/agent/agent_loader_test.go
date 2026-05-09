package agent

import (
	"testing"

	"eino-cli/backend/config"
)

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

func TestGetAgentConfig_RejectsInvalidName(t *testing.T) {
	cfg := config.Config{Agents: map[string]config.AgentConfig{}}
	_, err := GetAgentConfig(cfg, "../etc/passwd")
	if err == nil {
		t.Fatal("expected error for invalid agent name")
	}
}
