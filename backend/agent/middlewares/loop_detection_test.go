package middlewares

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestLoopDetection_StripsToolCallsAtHardLimit(t *testing.T) {
	mw := NewLoopDetection()
	mw.HardLimit = 3
	mw.WarnThreshold = 2

	dup := func() *adk.ChatModelAgentState {
		return &adk.ChatModelAgentState{
			Messages: []*schema.Message{
				{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
					{Function: schema.FunctionCall{Name: "read", Arguments: `{"file":"a"}`}},
				}},
			},
		}
	}

	for i := 0; i < mw.HardLimit-1; i++ {
		state := dup()
		_, _, err := mw.AfterModelRewriteState(context.Background(), state, nil)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if len(state.Messages[0].ToolCalls) == 0 {
			t.Fatalf("call %d: tool_calls stripped before hard limit", i)
		}
	}

	state := dup()
	_, _, err := mw.AfterModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("hard-limit call: %v", err)
	}
	if len(state.Messages[0].ToolCalls) != 0 {
		t.Fatalf("hard-limit call: expected tool_calls cleared, got %d",
			len(state.Messages[0].ToolCalls))
	}
}

func TestLoopDetection_DistinctCallsDoNotTrip(t *testing.T) {
	mw := NewLoopDetection()
	mw.HardLimit = 3

	for i := 0; i < 5; i++ {
		args := []byte{'{', '"', 'i', '"', ':', byte('0' + i), '}'}
		state := &adk.ChatModelAgentState{
			Messages: []*schema.Message{
				{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
					{Function: schema.FunctionCall{Name: "read", Arguments: string(args)}},
				}},
			},
		}
		_, _, err := mw.AfterModelRewriteState(context.Background(), state, nil)
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if len(state.Messages[0].ToolCalls) == 0 {
			t.Fatalf("call %d: distinct args wrongly tripped hard limit", i)
		}
	}
}
