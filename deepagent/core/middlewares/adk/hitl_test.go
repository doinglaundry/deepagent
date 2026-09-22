package middlewares

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestHITL_DenyRemovesGatedCallWithoutOrphanToolMessage(t *testing.T) {
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
	if len(out.Messages) != 2 {
		t.Fatalf("denied calls should not append orphan tool messages, got %d total", len(out.Messages))
	}
	if !strings.Contains(assistant.Content, "denied by user") {
		t.Fatalf("assistant content should explain denial, got %q", assistant.Content)
	}
}

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
