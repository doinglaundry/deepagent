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
		Metadata:               map[string]any{},
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

// The default chain (no gates) is exactly the always-on members in
// declaration order, ending with Trace then Clarification.
// Clarification MUST stay last (it rewrites assistant messages
// in-place); Trace MUST sit immediately before it (it captures the
// model's raw output, which Clarification's After hook would
// otherwise mangle).
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

// When summarization is off (default), no summarization middleware
// shows up in the chain.
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
