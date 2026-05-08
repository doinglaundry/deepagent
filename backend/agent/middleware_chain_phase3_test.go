package agent

import (
	"context"
	"reflect"
	"testing"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
)

// TestBuildChain_GatedMiddlewares wires every gating flag on (via cfg
// + deps) and verifies the resulting chain contains the expected
// middleware types and that the Clarification middleware remains last.
func TestBuildChain_GatedMiddlewares(t *testing.T) {
	rt := NewRuntimeContext()
	rt.ModelName = "primary"
	rt.AgentName = "default"
	rt.SubagentEnabled = true
	rt.IsPlanMode = true

	cfg := config.Config{
		Models: map[string]*config.ModelConfig{
			"primary": {Name: "primary", Provider: "kimi", SupportsVision: true},
		},
		Memory:     config.Memory{Enabled: true},
		TokenUsage: config.TokenUsage{Enabled: true},
		ToolSearch: config.ToolSearchConfig{Enabled: true},
	}
	deps := AgentDeps{
		DeferredToolNames: func() []string { return []string{"big-tool"} },
		HITLTools:         []string{"shell"},
	}

	chain, err := BuildChain(context.Background(), rt, cfg, deps, nil)
	if err != nil {
		t.Fatalf("BuildChain: %v", err)
	}

	wantOrder := []reflect.Type{
		reflect.TypeOf(&middlewares.AgentState{}),
		reflect.TypeOf(&middlewares.Title{}),
		reflect.TypeOf(&middlewares.ToolErrorHandling{}),
		reflect.TypeOf(&middlewares.LoopDetection{}),
		reflect.TypeOf(&middlewares.Memory{}),
		reflect.TypeOf(&middlewares.TokenUsage{}),
		reflect.TypeOf(&middlewares.ViewImage{}),
		reflect.TypeOf(&middlewares.DeferredTools{}),
		reflect.TypeOf(&middlewares.SubagentLimit{}),
		reflect.TypeOf(&middlewares.HITL{}),
		reflect.TypeOf(&middlewares.Clarification{}),
	}
	if len(chain.ChatModel) != len(wantOrder) {
		t.Fatalf("len(chain.ChatModel) = %d, want %d", len(chain.ChatModel), len(wantOrder))
	}
	for i, want := range wantOrder {
		got := reflect.TypeOf(chain.ChatModel[i])
		if got != want {
			t.Fatalf("slot %d: got %v, want %v", i, got, want)
		}
	}

	if len(chain.Agent) != 1 {
		t.Fatalf("expected 1 AgentMiddleware (Todo), got %d", len(chain.Agent))
	}
	if chain.Agent[0].AdditionalInstruction == "" {
		t.Fatalf("Todo middleware should set AdditionalInstruction")
	}
}

// TestBuildChain_NoGatesEmittedWhenDisabled checks that the gated slots
// disappear when their flags are off.
func TestBuildChain_NoGatesEmittedWhenDisabled(t *testing.T) {
	rt := NewRuntimeContext()
	rt.ModelName = "primary"
	cfg := config.Config{
		Models: map[string]*config.ModelConfig{
			"primary": {Name: "primary", Provider: "kimi", SupportsVision: false},
		},
	}

	chain, err := BuildChain(context.Background(), rt, cfg, AgentDeps{}, nil)
	if err != nil {
		t.Fatalf("BuildChain: %v", err)
	}
	for _, mw := range chain.ChatModel {
		switch mw.(type) {
		case *middlewares.Memory,
			*middlewares.TokenUsage,
			*middlewares.ViewImage,
			*middlewares.DeferredTools,
			*middlewares.SubagentLimit,
			*middlewares.HITL:
			t.Fatalf("gated middleware %T present without flag", mw)
		}
	}
	if len(chain.Agent) != 0 {
		t.Fatalf("expected no AgentMiddlewares without plan mode, got %d", len(chain.Agent))
	}
}
