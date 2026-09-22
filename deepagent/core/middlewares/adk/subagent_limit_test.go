package middlewares

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestSubagentLimit_TruncatesExcessTaskCalls(t *testing.T) {
	mw := NewSubagentLimit(2)

	calls := []schema.ToolCall{
		{ID: "1", Function: schema.FunctionCall{Name: "task", Arguments: "{}"}},
		{ID: "2", Function: schema.FunctionCall{Name: "task", Arguments: "{}"}},
		{ID: "3", Function: schema.FunctionCall{Name: "task", Arguments: "{}"}},
		{ID: "4", Function: schema.FunctionCall{Name: "read_file", Arguments: "{}"}},
		{ID: "5", Function: schema.FunctionCall{Name: "task", Arguments: "{}"}},
	}
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			{Role: schema.Assistant, ToolCalls: calls},
		},
	}

	_, _, err := mw.AfterModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := state.Messages[0].ToolCalls
	taskKept := 0
	otherKept := 0
	for _, c := range got {
		if c.Function.Name == "task" {
			taskKept++
		} else {
			otherKept++
		}
	}
	if taskKept != 2 {
		t.Fatalf("task() calls: got %d, want 2", taskKept)
	}
	if otherKept != 1 {
		t.Fatalf("non-task calls: got %d, want 1", otherKept)
	}
	if got[0].ID != "1" || got[1].ID != "2" {
		t.Fatalf("kept tasks should be the first two, got %s,%s", got[0].ID, got[1].ID)
	}
}

func TestSubagentLimit_PassesThroughWhenWithinLimit(t *testing.T) {
	mw := NewSubagentLimit(3)
	calls := []schema.ToolCall{
		{ID: "1", Function: schema.FunctionCall{Name: "task"}},
		{ID: "2", Function: schema.FunctionCall{Name: "task"}},
	}
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			{Role: schema.Assistant, ToolCalls: calls},
		},
	}
	_, _, err := mw.AfterModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(state.Messages[0].ToolCalls) != 2 {
		t.Fatalf("expected unchanged 2 calls, got %d", len(state.Messages[0].ToolCalls))
	}
}
