package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
)

// All gates ON: gated middlewares interleave between always-on prefix
// and the Trace + Clarification tail in the documented order.
func TestGetChatModelMiddlewares_GatedMiddlewares(t *testing.T) {
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
		HITLTools:              []string{"shell"},
		MaxConcurrentSubagents: 3,
	}

	cfg.RootDir = t.TempDir()
	chain := GetChatModelMiddlewares(context.Background(), "default", true, nil, cfg, nil)

	wantOrder := []reflect.Type{
		reflect.TypeOf(&middlewares.AgentState{}),
		reflect.TypeOf(&middlewares.Title{}),
		reflect.TypeOf(&middlewares.ToolCallObservability{}),
		reflect.TypeOf(&middlewares.ToolErrorHandling{}),
		nil, // patchtoolcalls.middleware — unexported, matched by string below
		reflect.TypeOf(&middlewares.LoopDetection{}),
		reflect.TypeOf(&middlewares.Memory{}),
		reflect.TypeOf(&middlewares.TokenUsage{}),
		reflect.TypeOf(&middlewares.DeferredTools{}),
		reflect.TypeOf(&middlewares.SubagentLimit{}),
		reflect.TypeOf(&middlewares.HITL{}),
		reflect.TypeOf(&middlewares.PlanReminder{}),
		reflect.TypeOf(&middlewares.TodoReminder{}),
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
}

// PlanReminder must run BEFORE TodoReminder so it modifies the agent's
// own system message instead of the todo reminder's prepended one.
// Drift here = plan-mode preamble grafted onto the wrong system msg.
func TestGetChatModelMiddlewares_PlanReminderBeforeTodoReminder(t *testing.T) {
	cfg := &config.Config{
		Models: map[string]*config.ModelConfig{
			"primary": {Name: "primary", Provider: "kimi"},
		},
	}
	cfg.RootDir = t.TempDir()
	chain := GetChatModelMiddlewares(context.Background(), "default", false, func() bool { return false }, cfg, nil)

	planIdx, todoIdx := -1, -1
	for i, mw := range chain {
		switch mw.(type) {
		case *middlewares.PlanReminder:
			planIdx = i
		case *middlewares.TodoReminder:
			todoIdx = i
		}
	}
	if planIdx < 0 {
		t.Fatalf("PlanReminder missing from chain")
	}
	if todoIdx < 0 {
		t.Fatalf("TodoReminder missing from chain")
	}
	if planIdx >= todoIdx {
		t.Fatalf("PlanReminder index %d must come before TodoReminder index %d", planIdx, todoIdx)
	}
}

// All gates OFF: only the always-on backbone survives.
func TestGetChatModelMiddlewares_NoGatesEmittedWhenDisabled(t *testing.T) {
	cfg := &config.Config{
		Models: map[string]*config.ModelConfig{
			"primary": {Name: "primary", Provider: "kimi", SupportsVision: false},
		},
	}

	chain := GetChatModelMiddlewares(context.Background(), "default", false, nil, cfg, nil)
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
}
