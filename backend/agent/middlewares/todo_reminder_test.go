package middlewares

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/schema"
)

func TestInjectTodoReminder_EmptyTodos_NoInjection(t *testing.T) {
	msgs := []*schema.Message{schema.UserMessage("hi")}
	out := injectTodoReminder(msgs, nil)
	if len(out) != 1 {
		t.Errorf("nil todos must not inject; got %d msgs", len(out))
	}
	out = injectTodoReminder(msgs, []deep.TODO{})
	if len(out) != 1 {
		t.Errorf("empty todos must not inject; got %d msgs", len(out))
	}
}

func TestInjectTodoReminder_HistoryHasWriteTodosCall_NoInjection(t *testing.T) {
	msgs := []*schema.Message{
		schema.UserMessage("plan refactor"),
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{Function: schema.FunctionCall{Name: "write_todos"}},
			},
		},
		schema.ToolMessage("ok", "tc-1"),
	}
	out := injectTodoReminder(msgs, []deep.TODO{{Content: "x", Status: "pending"}})
	if len(out) != len(msgs) {
		t.Errorf("must not inject when history still has write_todos call; got %d msgs", len(out))
	}
}

func TestInjectTodoReminder_HistoryScrubbed_InjectsReminder(t *testing.T) {
	// Summarisation has scrubbed write_todos out — only summary user msgs.
	msgs := []*schema.Message{
		schema.UserMessage("[summary] refactoring eino-cli"),
		schema.UserMessage("continue"),
	}
	todos := []deep.TODO{
		{Content: "Refactor lead_agent", Status: "in_progress"},
		{Content: "Add reminder middleware", Status: "pending"},
	}
	out := injectTodoReminder(msgs, todos)
	if len(out) != len(msgs)+1 {
		t.Fatalf("expected reminder prepended; got %d msgs", len(out))
	}
	if out[0].Role != schema.System {
		t.Errorf("reminder must be SystemMessage, got role=%s", out[0].Role)
	}
	if !strings.Contains(out[0].Content, todoReminderTag) {
		t.Errorf("reminder body missing idempotency tag; got:\n%s", out[0].Content)
	}
	if !strings.Contains(out[0].Content, "Refactor lead_agent") {
		t.Errorf("reminder body missing in_progress todo; got:\n%s", out[0].Content)
	}
	if !strings.Contains(out[0].Content, "[in_progress]") {
		t.Errorf("reminder body missing status tag; got:\n%s", out[0].Content)
	}
}

func TestInjectTodoReminder_AlreadyInjected_NoDuplicate(t *testing.T) {
	already := schema.SystemMessage(todoReminderTag + "\nprevious reminder")
	msgs := []*schema.Message{already, schema.UserMessage("continue")}
	out := injectTodoReminder(msgs, []deep.TODO{{Content: "x", Status: "pending"}})
	if len(out) != len(msgs) {
		t.Errorf("idempotency violated: prepended again. got %d msgs", len(out))
	}
}

func TestInjectTodoReminder_DoesNotMutateInputSlice(t *testing.T) {
	// The helper must return a new slice; mutating msgs in place would
	// corrupt state.Messages downstream.
	msgs := []*schema.Message{schema.UserMessage("hi")}
	origLen := len(msgs)
	_ = injectTodoReminder(msgs, []deep.TODO{{Content: "x", Status: "pending"}})
	if len(msgs) != origLen {
		t.Errorf("input msgs slice length changed: was %d, now %d", origLen, len(msgs))
	}
}

func TestRenderTodoReminder_BodyShape(t *testing.T) {
	body := renderTodoReminder([]deep.TODO{
		{Content: "Step A", Status: "completed"},
		{Content: "Step B", Status: "in_progress"},
	})
	want := []string{
		todoReminderTag,
		"</system_reminder>",
		"[completed] Step A",
		"[in_progress] Step B",
		"write_todos",
	}
	for _, s := range want {
		if !strings.Contains(body, s) {
			t.Errorf("renderTodoReminder body missing %q; got:\n%s", s, body)
		}
	}
}

func TestHasWriteTodosCall_OtherToolDoesNotCount(t *testing.T) {
	msgs := []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{Function: schema.FunctionCall{Name: "shell"}},
			},
		},
	}
	if hasWriteTodosCall(msgs) {
		t.Error("hasWriteTodosCall must not match unrelated tool calls")
	}
}

func TestHasReminderTag_NonSystemRoleNotMatched(t *testing.T) {
	// Only SystemMessage carries the tag legitimately. A user message that
	// echoes the tag string shouldn't fool the idempotency check (would
	// otherwise let users type the tag and disable reminders).
	msgs := []*schema.Message{schema.UserMessage(todoReminderTag)}
	if hasReminderTag(msgs) {
		t.Error("hasReminderTag must only match System role messages")
	}
}
