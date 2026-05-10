package agent

import (
	"context"
	"reflect"
	"testing"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
)

func makeChainTestRT() RuntimeContext {
	return RuntimeContext{
		ThinkingEnabled:        true,
		MaxConcurrentSubagents: 3,
		ModelName:              "primary",
		AgentName:              "default",
	}
}

func makeChainTestCfg() config.Config {
	return config.Config{
		DefaultModel: "primary",
		Models: map[string]*config.ModelConfig{
			"primary": {Name: "primary", Provider: "kimi"},
		},
	}
}

// Default chain ends with Trace then Clarification (the rewriter must run
// after Trace has captured the raw assistant message).
func TestGetChatModelMiddlewares_DefaultOrder(t *testing.T) {
	chain := GetChatModelMiddlewares(context.Background(), makeChainTestCfg(), NewMemoryAccessor(nil), makeChainTestRT())

	wantOrder := []reflect.Type{
		reflect.TypeOf(&middlewares.AgentState{}),
		reflect.TypeOf(&middlewares.Title{}),
		reflect.TypeOf(&middlewares.ToolErrorHandling{}),
		reflect.TypeOf(&middlewares.LoopDetection{}),
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

	if _, ok := chain[len(chain)-1].(*middlewares.Clarification); !ok {
		t.Fatalf("Clarification must be the last middleware")
	}
	if _, ok := chain[len(chain)-2].(*middlewares.Trace); !ok {
		t.Fatalf("Trace must sit immediately before Clarification")
	}
}

func TestGetChatModelMiddlewares_SummarizationDisabled(t *testing.T) {
	cfg := makeChainTestCfg()
	cfg.Summarization = config.Summarization{Enabled: false}
	chain := GetChatModelMiddlewares(context.Background(), cfg, NewMemoryAccessor(nil), makeChainTestRT())

	for _, mw := range chain {
		if reflect.TypeOf(mw).String() == "*summarization.middleware" {
			t.Fatalf("summarization slot should be skipped when disabled")
		}
	}
}
