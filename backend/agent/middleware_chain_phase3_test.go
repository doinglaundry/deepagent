package agent

import (
	"context"
	"reflect"
	"testing"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
)

// All gates ON: gated middlewares interleave between always-on prefix
// and the Trace + Clarification tail in the documented order.
func TestGetChatModelMiddlewares_GatedMiddlewares(t *testing.T) {
	rt := &RuntimeContext{
		MaxConcurrentSubagents: 3,
		AgentName:              "default",
		SubagentEnabled:        true,
		IsPlanMode:             true,
		HITLTools:              []string{"shell"},
	}

	cfg := &config.Config{
		Models: map[string]*config.ModelConfig{
			"primary": {Name: "primary", Provider: "kimi", SupportsVision: true},
		},
		Memory:     config.Memory{Enabled: true},
		TokenUsage: config.TokenUsage{Enabled: true},
		ToolSearch: config.ToolSearchConfig{
			Enabled:  true,
			Deferred: []config.DeferredToolEntry{{Name: "big-tool"}},
		},
	}

	cfg.RootDir = t.TempDir()
	chain := GetChatModelMiddlewares(context.Background(), cfg, rt, nil)

	wantOrder := []reflect.Type{
		reflect.TypeOf(&middlewares.AgentState{}),
		reflect.TypeOf(&middlewares.Title{}),
		reflect.TypeOf(&middlewares.ToolErrorHandling{}),
		reflect.TypeOf(&middlewares.LoopDetection{}),
		reflect.TypeOf(&middlewares.Memory{}),
		reflect.TypeOf(&middlewares.TokenUsage{}),
		reflect.TypeOf(&middlewares.DeferredTools{}),
		reflect.TypeOf(&middlewares.SubagentLimit{}),
		reflect.TypeOf(&middlewares.HITL{}),
		reflect.TypeOf(&middlewares.Trace{}),
		reflect.TypeOf(&middlewares.Clarification{}),
	}
	if len(chain) != len(wantOrder) {
		t.Fatalf("len(chain) = %d, want %d", len(chain), len(wantOrder))
	}
	for i, want := range wantOrder {
		if got := reflect.TypeOf(chain[i]); got != want {
			t.Fatalf("slot %d: got %v, want %v", i, got, want)
		}
	}

	agentMWs := GetAgentMiddleWares(rt)
	if len(agentMWs) != 1 {
		t.Fatalf("expected 1 AgentMiddleware (Todo), got %d", len(agentMWs))
	}
	if agentMWs[0].AdditionalInstruction == "" {
		t.Fatalf("Todo middleware should set AdditionalInstruction")
	}
}

// All gates OFF: only the always-on backbone survives.
func TestGetChatModelMiddlewares_NoGatesEmittedWhenDisabled(t *testing.T) {
	rt := &RuntimeContext{
		MaxConcurrentSubagents: 3,
	}
	cfg := &config.Config{
		Models: map[string]*config.ModelConfig{
			"primary": {Name: "primary", Provider: "kimi", SupportsVision: false},
		},
	}

	chain := GetChatModelMiddlewares(context.Background(), cfg, rt, nil)
	for _, mw := range chain {
		switch mw.(type) {
		case *middlewares.Memory,
			*middlewares.TokenUsage,
			*middlewares.DeferredTools,
			*middlewares.SubagentLimit,
			*middlewares.HITL:
			t.Fatalf("gated middleware %T present without flag", mw)
		}
	}

	if got := len(GetAgentMiddleWares(rt)); got != 0 {
		t.Fatalf("expected no AgentMiddlewares without plan mode, got %d", got)
	}
}
