package deepagent

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/config"
)

func TestNewRuntimeUnsupportedProvider(t *testing.T) {
	runtime, err := NewRuntime(context.Background(), &config.Config{
		DefaultModel: "primary",
		DefaultAgent: "default",
		Models: map[string]*config.ModelConfig{
			"primary": {Name: "primary", Provider: "unknown", Model: "foo", APIKey: "test-key", TimeoutSeconds: 30},
		},
		Agents: map[string]*config.AgentConfig{
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

func TestNewRuntimeExecuteEmptyPrompt(t *testing.T) {
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
		Agents: map[string]*config.AgentConfig{
			"default": {Name: "default", Model: "primary", Instruction: "You are a helpful assistant.", MaxIteration: 6},
		},
	}
	runtime, err := NewRuntime(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}

	_, err = runtime.ExecuteStream(context.Background(), "   ", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "prompt is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRuntimeExportImportHistory(t *testing.T) {
	src := &Runtime{
		history: []*schema.Message{
			schema.UserMessage("hello"),
			schema.AssistantMessage("world", nil),
		},
	}
	payload, err := src.ExportHistory()
	if err != nil {
		t.Fatalf("ExportHistory() error = %v", err)
	}
	dst := &Runtime{}
	if err := dst.RollbackToHistory(payload); err != nil {
		t.Fatalf("RollbackToHistory() error = %v", err)
	}
	if len(dst.history) != 2 {
		t.Fatalf("history len = %d, want 2", len(dst.history))
	}
	if dst.history[0].Content != "hello" || dst.history[1].Content != "world" {
		t.Fatalf("unexpected history: %#v", dst.history)
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

func TestCollectAgentEventsIgnoresToolResultsAndToolCallTurns(t *testing.T) {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	toolCallMsg := schema.AssistantMessage("I will search first", nil)
	toolCallMsg.ToolCalls = []schema.ToolCall{
		{ID: "call-1", Function: schema.FunctionCall{Name: "glob", Arguments: `{"pattern":"soul.md"}`}},
	}
	gen.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{Message: toolCallMsg}}})
	gen.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{Message: schema.ToolMessage("No files found", "call-1")}}})
	gen.Send(&adk.AgentEvent{Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{Message: schema.AssistantMessage("/Users/bytedance/go/src/content/LLM/soul.md", nil)}}})
	gen.Close()

	var chunks []string
	summary, err := collectAgentEventsWithSink(iter, func(chunk string) {
		chunks = append(chunks, chunk)
	})
	if err != nil {
		t.Fatalf("collectAgentEvents() error = %v", err)
	}
	if summary.Output != "/Users/bytedance/go/src/content/LLM/soul.md" {
		t.Fatalf("unexpected output: %q", summary.Output)
	}
	if len(chunks) != 1 || chunks[0] != summary.Output {
		t.Fatalf("unexpected chunks: %#v", chunks)
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
