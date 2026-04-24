package eino

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestNewDeepAgentRuntimeExecuteSuccess(t *testing.T) {
	runtime, err := NewDeepAgentRuntime(context.Background(), "deep-model", nil)
	if err != nil {
		t.Fatalf("NewDeepAgentRuntime() error = %v", err)
	}

	result, err := runtime.Execute(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success result, got %+v", result)
	}
	if !strings.Contains(result.Output, "deep runtime response from deep-model") {
		t.Fatalf("unexpected output: %q", result.Output)
	}
}

func TestNewDeepAgentRuntimeExecuteEmptyPrompt(t *testing.T) {
	runtime, err := NewDeepAgentRuntime(context.Background(), "deep-model", nil)
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

	summary, err := collectAgentEvents(iter)
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

	summary, err := collectAgentEvents(iter)
	if err != nil {
		t.Fatalf("collectAgentEvents() error = %v", err)
	}
	if !summary.Interrupted {
		t.Fatal("expected interrupted")
	}
}
