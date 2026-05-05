package middlewares

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// TestHITL_DenyRemovesGatedCallAndAppendsToolMessage verifies the Phase-6
// control flow: when the approval callback returns false, the gated tool
// call is removed from the assistant message and a synthetic tool result
// is appended so the next model turn knows the outcome.
func TestHITL_DenyRemovesGatedCallAndAppendsToolMessage(t *testing.T) {
	mw := NewHITL([]string{"shell.execute"}, func(_ context.Context, _ string, _ string) bool {
		return false
	})
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			schema.UserMessage("rm -rf /"),
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{
					{ID: "t1", Function: schema.FunctionCall{Name: "shell.execute", Arguments: `{"cmd":"rm -rf /"}`}},
				},
			},
		},
	}

	_, out, err := mw.AfterModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("AfterModelRewriteState: %v", err)
	}

	assistant := out.Messages[1]
	if len(assistant.ToolCalls) != 0 {
		t.Fatalf("expected denied tool call removed, got %d", len(assistant.ToolCalls))
	}
	if assistant.Content == "" {
		t.Fatalf("expected fallback Content when all calls denied")
	}
	if len(out.Messages) != 3 {
		t.Fatalf("expected synthetic tool message appended, got %d total", len(out.Messages))
	}
	tm := out.Messages[2]
	if tm.Role != schema.Tool || tm.ToolCallID != "t1" {
		t.Fatalf("unexpected synthetic tool message: %+v", tm)
	}
}

// TestHITL_ApproveLeavesCallsAlone verifies an approved call passes
// through unchanged and no synthetic tool message is appended.
func TestHITL_ApproveLeavesCallsAlone(t *testing.T) {
	mw := NewHITL([]string{"shell.execute"}, func(_ context.Context, _ string, _ string) bool {
		return true
	})
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "t1", Function: schema.FunctionCall{Name: "shell.execute", Arguments: `{}`}},
			},
		}},
	}
	_, out, err := mw.AfterModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("AfterModelRewriteState: %v", err)
	}
	if len(out.Messages[0].ToolCalls) != 1 {
		t.Fatalf("expected tool call preserved, got %d", len(out.Messages[0].ToolCalls))
	}
	if len(out.Messages) != 1 {
		t.Fatalf("expected no synthetic messages, got %d", len(out.Messages))
	}
}

// TestHITL_NonGatedCallsBypassCallback ensures tools outside the allowlist
// never trigger the approval callback (non-gated tools always pass).
func TestHITL_NonGatedCallsBypassCallback(t *testing.T) {
	called := false
	mw := NewHITL([]string{"shell.execute"}, func(_ context.Context, _ string, _ string) bool {
		called = true
		return false
	})
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "t1", Function: schema.FunctionCall{Name: "filesystem.read", Arguments: `{}`}},
			},
		}},
	}
	_, _, err := mw.AfterModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("AfterModelRewriteState: %v", err)
	}
	if called {
		t.Fatalf("approval callback invoked for non-gated tool")
	}
}
