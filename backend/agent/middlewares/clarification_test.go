package middlewares

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestClarification_RewritesAssistantMessage(t *testing.T) {
	mw := NewClarification()
	var capturedQ string
	mw.OnQuestion = func(_ context.Context, q string) { capturedQ = q }

	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			schema.UserMessage("hi"),
			{
				Role:    schema.Assistant,
				Content: "",
				ToolCalls: []schema.ToolCall{
					{
						ID: "c1",
						Function: schema.FunctionCall{
							Name:      AskClarificationToolName,
							Arguments: `{"question":"What's your timezone?"}`,
						},
					},
				},
			},
		},
	}

	_, out, err := mw.AfterModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("AfterModelRewriteState: %v", err)
	}

	last := out.Messages[len(out.Messages)-1]
	if len(last.ToolCalls) != 0 {
		t.Fatalf("expected ToolCalls to be cleared, got %d", len(last.ToolCalls))
	}
	if last.Content != "What's your timezone?" {
		t.Fatalf("unexpected Content: %q", last.Content)
	}
	if capturedQ != "What's your timezone?" {
		t.Fatalf("OnQuestion not invoked, got %q", capturedQ)
	}
}

func TestClarification_NoOpForOtherTools(t *testing.T) {
	mw := NewClarification()
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "x", Function: schema.FunctionCall{Name: "filesystem.read", Arguments: "{}"}},
			},
		}},
	}
	_, out, err := mw.AfterModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("AfterModelRewriteState: %v", err)
	}
	if len(out.Messages[0].ToolCalls) != 1 {
		t.Fatalf("expected ToolCalls preserved, got %d", len(out.Messages[0].ToolCalls))
	}
	if out.Messages[0].Content != "" {
		t.Fatalf("Content should not have been touched: %q", out.Messages[0].Content)
	}
}

func TestClarification_FallbackForUnparsedArgs(t *testing.T) {
	mw := NewClarification()
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "c1", Function: schema.FunctionCall{Name: AskClarificationToolName, Arguments: `not json`}},
			},
		}},
	}
	_, out, err := mw.AfterModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("AfterModelRewriteState: %v", err)
	}
	if out.Messages[0].Content == "" {
		t.Fatalf("expected fallback Content, got empty")
	}
}

func TestClarification_FormatsContextAndOptions(t *testing.T) {
	mw := NewClarification()
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					ID: "c1",
					Function: schema.FunctionCall{
						Name: AskClarificationToolName,
						Arguments: `{
							"question": "Which environment should I deploy to?",
							"clarification_type": "approach_choice",
							"context": "I need the target environment for the right configuration.",
							"options": ["development", "staging", "production"]
						}`,
					},
				},
			},
		}},
	}

	_, out, err := mw.AfterModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("AfterModelRewriteState: %v", err)
	}
	want := "I need the target environment for the right configuration.\n\nWhich environment should I deploy to?\n\n1. development\n2. staging\n3. production"
	if out.Messages[0].Content != want {
		t.Fatalf("formatted clarification:\ngot:  %q\nwant: %q", out.Messages[0].Content, want)
	}
}

func TestClarification_FormatsStringEncodedOptions(t *testing.T) {
	got := parseClarificationArgs(`{
		"question": "Should I continue?",
		"options": "[\"yes\", \"no\"]"
	}`)
	want := "Should I continue?\n\n1. yes\n2. no"
	if got != want {
		t.Fatalf("formatted clarification:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestClarification_QuestionFallbacks(t *testing.T) {
	if got := parseClarificationArgs(`{"prompt":"Pick a file."}`); got != "Pick a file." {
		t.Fatalf("prompt fallback: %q", got)
	}
	if got := parseClarificationArgs(`{"message":"Pick a file."}`); got != "Pick a file." {
		t.Fatalf("message fallback: %q", got)
	}
}
