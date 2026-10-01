package deepagents

import (
	"context"
	"fmt"
	"github.com/cloudwego/eino/compose"
	"testing"

	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/schema"
)

func TestLoopGuardStopsRepeatedToolsAndIsRunLocal(t *testing.T) {
	guard := middleware.NewLoopGuard()
	guard.HardLimit = 2
	for range 2 {
		m := &sequenceModel{responses: [][]*schema.Message{
			{schema.AssistantMessage("", []schema.ToolCall{{ID: "first", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
			{schema.AssistantMessage("stopping loop", []schema.ToolCall{{ID: "second", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
		}}
		tool := &countingTool{}
		a, err := NewRun(context.Background(), WithConfig(&Config{Model: m, EnableEagerTools: true, Middlewares: []middleware.Middleware{guard}, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, ParallelSafe: true}}}))
		if err != nil {
			t.Fatal(err)
		}
		result, err := a.Execute(context.Background(), []*schema.Message{schema.UserMessage("go")})
		if err != nil {
			t.Fatal(err)
		}
		if result.Content != "stopping loop" || tool.count.Load() != 1 || m.calls != 2 {
			t.Fatalf("result=%v tools=%d model=%d", result, tool.count.Load(), m.calls)
		}
	}
}

func TestLoopGuardRestoresWindowFromCheckpoint(t *testing.T) {
	ctx := context.Background()
	guard := middleware.NewLoopGuard()
	guard.HardLimit = 2
	counter := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "first", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
		{schema.AssistantMessage("stopping loop", []schema.ToolCall{{ID: "second", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
	}}
	cfg := Config{Model: m, RunID: "run", CheckpointStore: &checkpointMemory{}, Middlewares: []middleware.Middleware{guard}, ToolDescriptors: []tools.ToolDescriptor{{Tool: counter, RequiresApproval: true}}}
	first, err := NewRun(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(ctx)
	_, err = first.Execute(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok || len(info.InterruptContexts) != 1 {
		t.Fatalf("interrupt=%+v err=%v", info, err)
	}
	cfg.Conversation = first.conversation
	resumed, err := NewRun(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close(ctx)
	out, err := resumed.Execute(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "first", Approved: true}}))
	if err != nil || out == nil || out.Content != "stopping loop" || len(out.ToolCalls) != 0 || counter.count.Load() != 1 || m.calls != 2 {
		t.Fatalf("out=%v err=%v tools=%d model=%d", out, err, counter.count.Load(), m.calls)
	}
}

func TestLoopGuardDistinctCallsExpireFromWindow(t *testing.T) {
	guard := middleware.NewLoopGuard()
	guard.HardLimit = 2
	guard.WindowSize = 2
	m := &sequenceModel{}
	for i, args := range []string{`{"value":1}`, `{"value":2}`, `{"value":1}`} {
		m.responses = append(m.responses, []*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{ID: fmt.Sprint(i), Function: schema.FunctionCall{Name: "counter", Arguments: args}}})})
	}
	m.responses = append(m.responses, []*schema.Message{schema.AssistantMessage("done", nil)})
	counter := &countingTool{}
	a, err := NewRun(context.Background(), WithConfig(&Config{Model: m, Middlewares: []middleware.Middleware{guard}, ToolDescriptors: []tools.ToolDescriptor{{Tool: counter}}}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	out, err := a.Execute(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if err != nil || out == nil || out.Content != "done" || counter.count.Load() != 3 {
		t.Fatalf("out=%v err=%v calls=%d", out, err, counter.count.Load())
	}
}
