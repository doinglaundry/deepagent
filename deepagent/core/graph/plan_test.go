package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestRun_PlanRestoresFromCheckpointAfterContextCompaction(t *testing.T) {
	ctx := context.Background()
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "plan-call", Type: "function", Function: schema.FunctionCall{Name: "update_plan", Arguments: `{"todos":[{"content":"inspect repository","status":"in_progress"}]}`}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	published := 0
	cfg := Config{Model: m, RunID: "plan-run", CheckpointStore: &checkpointMemory{}, Middlewares: []middleware.Middleware{middleware.NewPlan(&middleware.PlanMiddlewareConfig{OnPlanUpdate: func(context.Context, middleware.PlanUpdate) error { published++; return nil }})}}
	first, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Emit = nil
	first.emit = func(_ context.Context, event types.RuntimeEvent) error {
		if event.Kind == "tool_end" {
			first.Interrupt()
		}
		return nil
	}
	_, err = first.Run(ctx, []*schema.Message{schema.UserMessage("inspect")}, WithCheckpointID("plan-checkpoint"))
	if _, ok := compose.ExtractInterruptInfo(err); !ok {
		t.Fatalf("expected checkpoint: %v", err)
	}
	if published != 1 || len(first.state.Plan) != 1 {
		t.Fatalf("published=%d state=%+v", published, first.state.Plan)
	}
	// Model context no longer contains the tool exchange, as after compaction.
	cfg.Conversation = conversation.New("thread", nil, nil, nil)
	if err := cfg.Conversation.AddHistory(ctx, "plan-run", schema.UserMessage("compacted summary")); err != nil {
		t.Fatal(err)
	}
	restored, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	out, err := restored.Run(ctx, nil, WithCheckpointID("plan-checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "done" || published != 1 || m.calls != 2 {
		t.Fatalf("output=%v published=%d model=%d", out, published, m.calls)
	}
	if len(restored.state.Plan) != 1 || restored.state.Plan[0].Step != "inspect repository" {
		t.Fatalf("restored plan=%+v", restored.state.Plan)
	}
	reminders, instructions := 0, 0
	for _, message := range m.inputs[1] {
		if strings.Contains(message.Content, "[in_progress] inspect repository") {
			reminders++
		}
		if strings.Contains(message.Content, "<plan_mode>") && strings.Contains(message.Content, "update_plan") {
			instructions++
		}
	}
	if reminders != 1 || instructions != 1 {
		t.Fatalf("reminders=%d instructions=%d", reminders, instructions)
	}
	for _, message := range restored.conversation.History(ctx) {
		if strings.Contains(message.Content, "<plan_mode>") {
			t.Fatal("planning instruction polluted durable history")
		}
	}
}

func TestRun_PlanEventsUseGraphSequenceAndDeliveryErrorsAreFatal(t *testing.T) {
	for _, failDelivery := range []bool{false, true} {
		m := &sequenceModel{responses: [][]*schema.Message{
			{schema.AssistantMessage("", []schema.ToolCall{{ID: "plan", Function: schema.FunctionCall{Name: "update_plan", Arguments: `{"plan":"inspect"}`}}})},
			{schema.AssistantMessage("done", nil)},
		}}
		var events []types.RuntimeEvent
		want := errors.New("event transport failed")
		a, err := New(context.Background(), WithConfig(&Config{Model: m, Middlewares: []middleware.Middleware{middleware.NewPlan(nil)}, Emit: func(_ context.Context, event types.RuntimeEvent) error {
			events = append(events, event)
			if failDelivery && event.Kind == "plan_updated" {
				return want
			}
			return nil
		}}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")})
		if failDelivery {
			if !errors.Is(err, want) || m.calls != 1 {
				t.Fatalf("delivery error swallowed: err=%v calls=%d", err, m.calls)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		planIndex, startIndex, endIndex, count := -1, -1, -1, 0
		for i, event := range events {
			if event.Sequence != uint64(i+1) {
				t.Fatalf("sequence=%d position=%d", event.Sequence, i)
			}
			switch event.Kind {
			case "tool_start":
				startIndex = i
			case "plan_updated":
				planIndex = i
				count++
			case "tool_end":
				endIndex = i
			}
		}
		if count != 1 || startIndex < 0 || planIndex <= startIndex || (!failDelivery && endIndex <= planIndex) {
			t.Fatalf("start=%d plan=%d end=%d count=%d", startIndex, planIndex, endIndex, count)
		}
	}
}
