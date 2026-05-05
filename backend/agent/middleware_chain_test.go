package agent

import (
	"context"
	"reflect"
	"testing"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
)

// TestBuildChain_DefaultOrder verifies the always-on chain ends with the
// Clarification middleware (Python invariant) and contains the documented
// always-on members in the expected order.
func TestBuildChain_DefaultOrder(t *testing.T) {
	chain, err := BuildChain(context.Background(), ChainOptions{
		Runtime:   NewRuntimeContext(),
		ModelName: "primary",
		AgentName: "default",
		Config: config.Config{
			DefaultModel: "primary",
			Models: map[string]*config.ModelConfig{
				"primary": {Name: "primary", Provider: "kimi"},
			},
		},
	})
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

	// Sanity check: Clarification must be last.
	if _, ok := chain.ChatModel[len(chain.ChatModel)-1].(*middlewares.Clarification); !ok {
		t.Fatalf("Clarification must be the last middleware")
	}
}

// TestBuildChain_SummarizationDisabled confirms the summarization slot is
// omitted when AppConfig.Summarization.Enabled is false (Phase 2 default).
func TestBuildChain_SummarizationDisabled(t *testing.T) {
	chain, err := BuildChain(context.Background(), ChainOptions{
		Runtime:   NewRuntimeContext(),
		ModelName: "primary",
		AgentName: "default",
		Config: config.Config{
			Models: map[string]*config.ModelConfig{
				"primary": {Name: "primary", Provider: "kimi"},
			},
		},
		AppConfig: &AppConfig{
			Summarization: SummarizationConfig{Enabled: false},
		},
	})
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
	_, err := BuildChain(context.Background(), ChainOptions{
		Runtime:   NewRuntimeContext(),
		ModelName: "primary",
		AgentName: "default",
		Config:    config.Config{},
		AppConfig: &AppConfig{
			Summarization: SummarizationConfig{Enabled: true},
		},
		// SummaryModel intentionally nil.
	})
	if err == nil {
		t.Fatalf("expected error when summarization is enabled without a model")
	}
}
