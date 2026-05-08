package agent

import (
	"context"
	"reflect"
	"testing"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
)

func makeChainTestRT() RuntimeContext {
	rt := NewRuntimeContext()
	rt.ModelName = "primary"
	rt.AgentName = "default"
	return rt
}

func makeChainTestCfg() config.Config {
	return config.Config{
		DefaultModel: "primary",
		Models: map[string]*config.ModelConfig{
			"primary": {Name: "primary", Provider: "kimi"},
		},
	}
}

// TestBuildChain_DefaultOrder verifies the always-on chain ends with the
// Clarification middleware (Python invariant) and contains the documented
// always-on members in the expected order.
func TestBuildChain_DefaultOrder(t *testing.T) {
	chain, err := BuildChain(context.Background(), makeChainTestRT(), makeChainTestCfg(), AgentDeps{}, nil)
	if err != nil {
		t.Fatalf("BuildChain: unexpected error: %v", err)
	}

	if len(chain.ChatModel) != 5 {
		t.Fatalf("expected 5 always-on middlewares, got %d", len(chain.ChatModel))
	}

	expectedTypes := []reflect.Type{
		reflect.TypeOf(&middlewares.AgentState{}),
		reflect.TypeOf(&middlewares.Title{}),
		reflect.TypeOf(&middlewares.ToolErrorHandling{}),
		reflect.TypeOf(&middlewares.LoopDetection{}),
		reflect.TypeOf(&middlewares.Clarification{}),
	}
	for i, want := range expectedTypes {
		got := reflect.TypeOf(chain.ChatModel[i])
		if got != want {
			t.Fatalf("slot %d: got %v, want %v", i, got, want)
		}
	}

	if _, ok := chain.ChatModel[len(chain.ChatModel)-1].(*middlewares.Clarification); !ok {
		t.Fatalf("Clarification must be the last middleware")
	}
}

// TestBuildChain_SummarizationDisabled confirms the summarization slot is
// omitted when cfg.Summarization.Enabled is false (Phase 2 default).
func TestBuildChain_SummarizationDisabled(t *testing.T) {
	cfg := makeChainTestCfg()
	cfg.Summarization = config.Summarization{Enabled: false}
	chain, err := BuildChain(context.Background(), makeChainTestRT(), cfg, AgentDeps{}, nil)
	if err != nil {
		t.Fatalf("BuildChain: unexpected error: %v", err)
	}

	for _, mw := range chain.ChatModel {
		if reflect.TypeOf(mw).String() == "*summarization.middleware" {
			t.Fatalf("summarization slot should be skipped when disabled")
		}
	}
}

// TestBuildChain_SummarizationEnabledWithoutModel surfaces the configuration
// error when summarization is on but no SummaryModel is provided.
func TestBuildChain_SummarizationEnabledWithoutModel(t *testing.T) {
	cfg := makeChainTestCfg()
	cfg.Summarization = config.Summarization{Enabled: true}
	_, err := BuildChain(context.Background(), makeChainTestRT(), cfg, AgentDeps{}, nil)
	if err == nil {
		t.Fatalf("expected error when summarization is enabled without a model")
	}
}
