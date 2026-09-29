package graph

import (
	"context"
	"fmt"
	"testing"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func usageReply(text string, calls ...schema.ToolCall) *schema.Message {
	m := schema.AssistantMessage(text, calls)
	m.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}}
	return m
}

func TestRun_TokenEventsAccumulateWithoutChangingContextUsage(t *testing.T) {
	ctx := context.Background()
	m := &sequenceModel{responses: [][]*schema.Message{
		{usageReply("", schema.ToolCall{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}})},
		{usageReply("done")}, {usageReply("new run")},
	}}
	var totals []types.Usage
	a, err := New(ctx, WithConfig(&Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: &countingTool{}}}, Emit: func(_ context.Context, e types.RuntimeEvent) error {
		if e.Kind == "tokens" {
			totals = append(totals, e.Data.(types.Usage))
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	for i, want := range []int64{10, 5} {
		if i > 0 {
			cfg := a.cfg
			cfg.Conversation = a.conversation
			a, err = New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(ctx)
		}
		_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("go")})
		if err != nil {
			t.Fatal(err)
		}
		if a.state.Usage.TotalTokens != want {
			t.Fatalf("run %d cumulative=%+v", i, a.state.Usage)
		}
		if got := a.conversation.ContextUsage(); got.LastModelTotal != 5 || got.CurrentTotal != 5 {
			t.Fatalf("context usage must remain last request: %+v", got)
		}
	}
	if len(totals) != 3 || totals[0].TotalTokens != 5 || totals[1] != (types.Usage{PromptTokens: 6, CompletionTokens: 4, TotalTokens: 10}) || totals[2].TotalTokens != 5 {
		t.Fatalf("token events=%+v", totals)
	}
}

func TestCheckpoint_ResumeContinuesCumulativeUsage(t *testing.T) {
	ctx := context.Background()
	m := &sequenceModel{responses: [][]*schema.Message{
		{usageReply("", schema.ToolCall{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}})},
		{usageReply("done")},
	}}
	cfg := Config{ThreadID: "thread", RunID: "run", Model: m, CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.ToolDescriptor{{Tool: &countingTool{}, RequiresApproval: true}}}
	first, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.Run(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatal(err)
	}
	if err = first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// A different Conversation forces restoration from the checkpoint snapshot.
	next, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(ctx)
	if err = next.conversation.AddHistory(ctx, "run", first.conversation.History(ctx)...); err != nil {
		t.Fatal(err)
	}
	_, err = next.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{Approved: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if next.state.Usage != (types.Usage{PromptTokens: 6, CompletionTokens: 4, TotalTokens: 10}) {
		t.Fatalf("restored usage=%+v", next.state.Usage)
	}
}

func TestRun_LegacyExtraUsageUsesConversation(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		t.Run(fmt.Sprint(metadata), func(t *testing.T) {
			response := schema.AssistantMessage("done", nil)
			response.Extra = map[string]any{"prompt_tokens": int64(7), "completion_tokens": float64(3)}
			want := int64(10)
			if metadata {
				response.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}}
				want = 5
			}
			var totals []types.Usage
			a, err := New(context.Background(), WithConfig(&Config{Model: &sequenceModel{responses: [][]*schema.Message{{response}}}, Emit: func(_ context.Context, event types.RuntimeEvent) error {
				if event.Kind == "tokens" {
					totals = append(totals, event.Data.(types.Usage))
				}
				return nil
			}}))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(context.Background())
			if _, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}); err != nil {
				t.Fatal(err)
			}
			if len(totals) != 1 || totals[0].TotalTokens != want || a.state.Usage != totals[0] || a.conversation.RunUsage() != totals[0] || a.conversation.ContextUsage().LastModelTotal != want {
				t.Fatalf("events=%+v snapshot=%+v context=%+v", totals, a.state.Usage, a.conversation.ContextUsage())
			}
		})
	}
}
