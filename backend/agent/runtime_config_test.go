package agent

import (
	"strings"
	"testing"

	"eino-cli/backend/config"
)

func setterTestCfg() *config.Config {
	return &config.Config{
		DefaultAgent: "default",
		DefaultModel: "primary",
		Models: map[string]*config.ModelConfig{
			"primary":   {Name: "primary", Provider: "kimi"},
			"secondary": {Name: "secondary", Provider: "kimi"},
		},
		Agents: map[string]*config.AgentConfig{
			"default": {Name: "default", Model: "primary", MaxIteration: 6},
			"alt":     {Name: "alt", Model: "secondary", MaxIteration: 6},
		},
	}
}

func TestNewRuntimeContext_BaselineDefaults(t *testing.T) {
	rt, err := NewRuntimeContext(setterTestCfg())
	if err != nil {
		t.Fatalf("NewRuntimeContext: %v", err)
	}
	if rt.AgentName != "default" {
		t.Errorf("AgentName = %q, want %q", rt.AgentName, "default")
	}
	if rt.AgentConfig == nil || rt.ModelCfg == nil {
		t.Errorf("AgentConfig/ModelCfg must be resolved")
	}
	if rt.MaxConcurrentSubagents != 3 {
		t.Errorf("default MaxConcurrentSubagents = %d, want 3", rt.MaxConcurrentSubagents)
	}
	if rt.IsPlanMode || rt.SubagentEnabled || rt.HITLTools != nil {
		t.Errorf("non-baseline fields must be zero")
	}
}

func TestRuntimeContext_SetPlanMode(t *testing.T) {
	rt, _ := NewRuntimeContext(setterTestCfg())
	rt.SetPlanMode(true)
	if !rt.IsPlanMode {
		t.Errorf("SetPlanMode(true) didn't stick")
	}
	rt.SetPlanMode(false)
	if rt.IsPlanMode {
		t.Errorf("SetPlanMode(false) didn't stick")
	}
}

func TestRuntimeContext_SetSubagentEnabled(t *testing.T) {
	rt, _ := NewRuntimeContext(setterTestCfg())
	rt.SetSubagentEnabled(true)
	if !rt.SubagentEnabled {
		t.Errorf("SetSubagentEnabled(true) didn't stick")
	}
}

func TestRuntimeContext_SetMaxConcurrentSubagents(t *testing.T) {
	rt, _ := NewRuntimeContext(setterTestCfg())
	rt.SetMaxConcurrentSubagents(7)
	if rt.MaxConcurrentSubagents != 7 {
		t.Errorf("MaxConcurrentSubagents = %d, want 7", rt.MaxConcurrentSubagents)
	}
	// non-positive normalises to 3 — same baseline default.
	rt.SetMaxConcurrentSubagents(0)
	if rt.MaxConcurrentSubagents != 3 {
		t.Errorf("zero-input should normalise to 3, got %d", rt.MaxConcurrentSubagents)
	}
	rt.SetMaxConcurrentSubagents(-2)
	if rt.MaxConcurrentSubagents != 3 {
		t.Errorf("negative-input should normalise to 3, got %d", rt.MaxConcurrentSubagents)
	}
}

func TestRuntimeContext_SetHITLTools(t *testing.T) {
	rt, _ := NewRuntimeContext(setterTestCfg())
	rt.SetHITLTools([]string{"shell", "python"})
	if len(rt.HITLTools) != 2 || rt.HITLTools[0] != "shell" {
		t.Errorf("HITLTools not applied: %v", rt.HITLTools)
	}
}

func TestRuntimeContext_SetAgentName_Success(t *testing.T) {
	cfg := setterTestCfg()
	rt, _ := NewRuntimeContext(cfg)
	if err := rt.SetAgentName(cfg, "alt"); err != nil {
		t.Fatalf("SetAgentName(alt): %v", err)
	}
	if rt.AgentName != "alt" || rt.AgentConfig.Name != "alt" {
		t.Errorf("AgentName/AgentConfig.Name didn't refresh: %q / %q", rt.AgentName, rt.AgentConfig.Name)
	}
	if rt.ModelCfg.Name != "secondary" {
		t.Errorf("ModelCfg didn't refresh; got %q", rt.ModelCfg.Name)
	}
}

func TestRuntimeContext_SetAgentName_AgentMissing_KeepsPreviousState(t *testing.T) {
	cfg := setterTestCfg()
	rt, _ := NewRuntimeContext(cfg)
	prevName, prevAgent, prevModel := rt.AgentName, rt.AgentConfig, rt.ModelCfg

	err := rt.SetAgentName(cfg, "no-such-agent")
	if err == nil {
		t.Fatal("expected error for missing agent")
	}
	if !strings.Contains(err.Error(), "load agent fail") {
		t.Errorf("unexpected error message: %v", err)
	}
	// All three fields untouched on failure.
	if rt.AgentName != prevName || rt.AgentConfig != prevAgent || rt.ModelCfg != prevModel {
		t.Errorf("rt fields mutated on failure; want atomic rollback")
	}
}

func TestRuntimeContext_SetAgentName_ModelMissing_KeepsPreviousState(t *testing.T) {
	cfg := setterTestCfg()
	cfg.Agents["bad"] = &config.AgentConfig{Name: "bad", Model: "no-such-model", MaxIteration: 6}
	rt, _ := NewRuntimeContext(cfg)
	prevName, prevAgent, prevModel := rt.AgentName, rt.AgentConfig, rt.ModelCfg

	if err := rt.SetAgentName(cfg, "bad"); err == nil {
		t.Fatal("expected error for missing model")
	}
	if rt.AgentName != prevName || rt.AgentConfig != prevAgent || rt.ModelCfg != prevModel {
		t.Errorf("rt fields mutated when model lookup failed")
	}
}

func TestRuntimeContext_Clone_BasicEquality(t *testing.T) {
	rt, _ := NewRuntimeContext(setterTestCfg())
	rt.SetPlanMode(true)
	rt.SetHITLTools([]string{"shell"})

	clone := rt.Clone()

	if clone == rt {
		t.Errorf("Clone must not return the same pointer")
	}
	if clone.AgentName != rt.AgentName || clone.IsPlanMode != rt.IsPlanMode {
		t.Errorf("Clone fields don't match: %+v vs %+v", clone, rt)
	}
}

func TestRuntimeContext_Clone_HITLToolsIndependent(t *testing.T) {
	rt, _ := NewRuntimeContext(setterTestCfg())
	rt.SetHITLTools([]string{"shell"})

	clone := rt.Clone()
	clone.SetHITLTools(append(clone.HITLTools, "python"))

	if len(rt.HITLTools) != 1 {
		t.Errorf("parent HITLTools polluted: %v", rt.HITLTools)
	}
	if len(clone.HITLTools) != 2 {
		t.Errorf("clone HITLTools didn't update: %v", clone.HITLTools)
	}
}

func TestRuntimeContext_Clone_SharesAgentConfigPointer(t *testing.T) {
	// AgentConfig / ModelCfg are immutable lookup results; shared pointers
	// across forks is intentional. SetAgentName replaces the pointer, never
	// mutates the pointee, so sharing is safe.
	rt, _ := NewRuntimeContext(setterTestCfg())
	clone := rt.Clone()
	if clone.AgentConfig != rt.AgentConfig {
		t.Errorf("AgentConfig pointer should be shared between parent and clone")
	}
	if clone.ModelCfg != rt.ModelCfg {
		t.Errorf("ModelCfg pointer should be shared between parent and clone")
	}
}
