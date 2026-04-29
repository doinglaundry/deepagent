package eino

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/config"
)

func TestBuildRuntimeUnsupportedProvider(t *testing.T) {
	runtime, err := BuildRuntime(context.Background(), config.Config{
		DefaultModel: "primary",
		DefaultAgent: "default",
		Models: map[string]*config.ModelConfig{
			"primary": {Name: "primary", Provider: "unknown", Model: "foo", APIKeyEnv: "FOO_KEY", TimeoutSeconds: 30},
		},
		Agents: map[string]config.AgentConfig{
			"default": {Name: "deep-agent", Instruction: "You are a helpful assistant.", MaxIteration: 6},
		},
	}, nil)
	if err == nil {
		t.Fatalf("expected error, got runtime=%v", runtime)
	}
	if !strings.Contains(err.Error(), "unsupported model provider") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNewDeepAgentRuntimeExecuteEmptyPrompt(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	runtime, err := NewDeepAgentRuntime(context.Background(), config.ModelConfig{
		Name:           "primary",
		Provider:       "claude",
		Model:          "claude-sonnet-4-6",
		APIKeyEnv:      "ANTHROPIC_API_KEY",
		TimeoutSeconds: 30,
	}, config.AgentConfig{
		Name:         "deep-agent",
		Instruction:  "You are a helpful assistant.",
		MaxIteration: 6,
	}, nil)
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
