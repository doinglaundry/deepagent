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

	wantOrder := []string{
		"*middlewares.AgentState",
		"*middlewares.ToolCallObservability",
		"*middlewares.ToolErrorHandling",
		"patchtoolcalls",
		"*middlewares.LoopDetection",
		"*middlewares.Memory",
		"*middlewares.TokenUsage",
		"*middlewares.HITL",
		"*summarization.middleware",
		"*middlewares.PlanReminder",
		"*middlewares.TodoReminder",
		"*middlewares.SandboxMiddleware",
		"*middlewares.MessagesLog",
		"*middlewares.Trace",
		"*middlewares.Clarification",
	}
	if len(chain) != len(wantOrder) {
		t.Fatalf("len(chain) = %d, want %d", len(chain), len(wantOrder))
	}
	for i, want := range wantOrder {
		got := reflect.TypeOf(chain[i]).String()
		if !strings.Contains(got, want) {
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

func TestGetChatModelMiddlewares_SummarizationSkippedWithoutModel(t *testing.T) {
	cfg := makeChainTestCfg()
	cfg.Models["primary"].Provider = "unknown"
	chain := GetChatModelMiddlewares(context.Background(), "default", false, nil, cfg, nil, nil)

	for _, mw := range chain {
		if reflect.TypeOf(mw).String() == "*summarization.middleware" {
			t.Fatalf("summarization slot should be skipped without a summary model")
		}
	}
}
