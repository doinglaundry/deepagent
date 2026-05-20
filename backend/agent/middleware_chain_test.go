package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
)

func makeChainTestCfg() *config.Config {
	return &config.Config{
		DefaultModel: "primary",
		Models: map[string]*config.ModelConfig{
			"primary": {Name: "primary", Provider: "kimi"},
		},
	}
}

// Default chain ends with Trace then Clarification (the rewriter must run
// after Trace has captured the raw assistant message).
func TestGetChatModelMiddlewares_DefaultOrder(t *testing.T) {
	chain := GetChatModelMiddlewares(context.Background(), "default", false, nil, makeChainTestCfg(), nil, nil)

	wantOrder := []reflect.Type{
		reflect.TypeOf(&middlewares.AgentState{}),
		reflect.TypeOf(&middlewares.ToolCallObservability{}),
		reflect.TypeOf(&middlewares.ToolErrorHandling{}),
		nil, // patchtoolcalls.middleware — unexported, matched by string below
		reflect.TypeOf(&middlewares.LoopDetection{}),
		reflect.TypeOf(&middlewares.PlanReminder{}),
		reflect.TypeOf(&middlewares.TodoReminder{}),
		reflect.TypeOf(&middlewares.SandboxMiddleware{}),
		reflect.TypeOf(&middlewares.MessagesLog{}),
		reflect.TypeOf(&middlewares.Trace{}),
		reflect.TypeOf(&middlewares.Clarification{}),
	}
	if len(chain) != len(wantOrder) {
		t.Fatalf("len(chain) = %d, want %d", len(chain), len(wantOrder))
	}
	for i, want := range wantOrder {
		got := reflect.TypeOf(chain[i])
		if want == nil {
			if !strings.Contains(got.String(), "patchtoolcalls") {
				t.Fatalf("slot %d: got %v, want patchtoolcalls middleware", i, got)
			}
			continue
		}
		if got != want {
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
	chain := GetChatModelMiddlewares(context.Background(), "default", false, nil, cfg, nil, nil)

	for _, mw := range chain {
		if reflect.TypeOf(mw).String() == "*summarization.middleware" {
			t.Fatalf("summarization slot should be skipped when disabled")
		}
	}
}
