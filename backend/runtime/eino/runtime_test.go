package eino

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/config"
)

func planModeTestCfg() *config.Config {
	return &config.Config{
		DefaultModel: "primary",
		DefaultAgent: "default",
		Models: map[string]*config.ModelConfig{
			"primary": {
				Name:           "primary",
				Provider:       "claude",
				Model:          "claude-sonnet-4-6",
				APIKey:         "test-key",
				TimeoutSeconds: 30,
			},
		},
		Agents: map[string]config.AgentConfig{
			"default": {Name: "default", Model: "primary", Instruction: "You are helpful.", MaxIteration: 6},
		},
	}
}

func TestNewDeepAgentRuntimeUnsupportedProvider(t *testing.T) {
	runtime, err := NewDeepAgentRuntime(context.Background(), &config.Config{
		DefaultModel: "primary",
		DefaultAgent: "default",
		Models: map[string]*config.ModelConfig{
			"primary": {Name: "primary", Provider: "unknown", Model: "foo", APIKey: "test-key", TimeoutSeconds: 30},
		},
		Agents: map[string]config.AgentConfig{
			"default": {Name: "default", Model: "primary", Instruction: "You are a helpful assistant.", MaxIteration: 6},
		},
	})
	if err == nil {
		t.Fatalf("expected error, got runtime=%v", runtime)
	}
	if !strings.Contains(err.Error(), "unsupported model provider") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewDeepAgentRuntimeExecuteEmptyPrompt(t *testing.T) {
	cfg := &config.Config{
		DefaultModel: "primary",
		DefaultAgent: "default",
		Models: map[string]*config.ModelConfig{
			"primary": {
				Name:           "primary",
				Provider:       "claude",
				Model:          "claude-sonnet-4-6",
				APIKey:         "test-key",
				TimeoutSeconds: 30,
			},
		},
		Agents: map[string]config.AgentConfig{
			"default": {Name: "default", Model: "primary", Instruction: "You are a helpful assistant.", MaxIteration: 6},
		},
	}
	runtime, err := NewDeepAgentRuntime(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDeepAgentRuntime() error = %v", err)
	}

	_, err = runtime.Execute(context.Background(), "   ")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCollectAgentEventsAggregatesOutput(t *testing.T) {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{Message: schema.AssistantMessage("first", nil)}}})
	gen.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{Message: schema.AssistantMessage("second", nil)}}})
	gen.Close()

	summary, err := collectAgentEventsWithSink(iter, nil)
	if err != nil {
		t.Fatalf("collectAgentEvents() error = %v", err)
	}
	if summary.Interrupted {
		t.Fatal("expected not interrupted")
	}
	if summary.Output != "first\nsecond" {
		t.Fatalf("unexpected output: %q", summary.Output)
	}
}

func TestCollectAgentEventsInterrupted(t *testing.T) {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(&adk.AgentEvent{Action: &adk.AgentAction{Interrupted: &adk.InterruptInfo{Data: "need approval"}}})
	gen.Close()

	summary, err := collectAgentEventsWithSink(iter, nil)
	if err != nil {
		t.Fatalf("collectAgentEvents() error = %v", err)
	}
	if !summary.Interrupted {
		t.Fatal("expected interrupted")
	}
}

// SetPlanMode rebuilds the lead agent: r.runner / r.trace pointers must
// change and r.rt.IsPlanMode must reflect the new value.
func TestDeepAgentRuntime_SetPlanMode_ToggleRebuilds(t *testing.T) {
	rt, err := NewDeepAgentRuntime(context.Background(), planModeTestCfg())
	if err != nil {
		t.Fatalf("NewDeepAgentRuntime: %v", err)
	}
	r := rt.(*DeepAgentRuntime)

	prevRunner, prevTrace := r.runner, r.trace
	if err := r.SetPlanMode(context.Background(), true); err != nil {
		t.Fatalf("SetPlanMode(true): %v", err)
	}
	if !r.rt.IsPlanMode {
		t.Errorf("rt.IsPlanMode didn't flip to true")
	}
	if r.runner == prevRunner {
		t.Errorf("runner pointer should change after rebuild")
	}
	if r.trace == prevTrace {
		t.Errorf("trace pointer should change after rebuild")
	}
}

// SetPlanMode with the same value is a no-op: runner pointer must NOT
// change (no wasted rebuild).
func TestDeepAgentRuntime_SetPlanMode_NoOp(t *testing.T) {
	rt, err := NewDeepAgentRuntime(context.Background(), planModeTestCfg())
	if err != nil {
		t.Fatalf("NewDeepAgentRuntime: %v", err)
	}
	r := rt.(*DeepAgentRuntime)

	prevRunner := r.runner
	if err := r.SetPlanMode(context.Background(), false); err != nil {
		t.Fatalf("SetPlanMode(false) baseline: %v", err)
	}
	if r.runner != prevRunner {
		t.Errorf("no-op SetPlanMode shouldn't rebuild runner")
	}
}

// SetPlanMode is the only writer to r.rt; it serialises with ExecuteStream
// via r.mu. Run -race to catch the snapshot-runner regression.
func TestDeepAgentRuntime_SetPlanMode_Race(t *testing.T) {
	rt, err := NewDeepAgentRuntime(context.Background(), planModeTestCfg())
	if err != nil {
		t.Fatalf("NewDeepAgentRuntime: %v", err)
	}
	r := rt.(*DeepAgentRuntime)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			_ = r.SetPlanMode(context.Background(), i%2 == 0)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			// Empty prompt fast-rejects without invoking the runner, but
			// still touches r.history and snapshots r.runner under r.mu.
			_, _ = r.ExecuteStream(context.Background(), "", nil)
		}
	}()
	wg.Wait()
}
