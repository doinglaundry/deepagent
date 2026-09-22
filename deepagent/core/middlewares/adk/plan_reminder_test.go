package middlewares

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// Plan mode off → BeforeModelRewriteState is a no-op even on a system
// message that lacks the tag. Cheapest path; runs every turn when
// users haven't opted into plan mode.
func TestPlanReminder_OffIsNoOp(t *testing.T) {
	mw := NewPlanReminder(func() bool { return false })
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{schema.SystemMessage("you are helpful")},
	}
	original := state.Messages[0].Content

	_, _, err := mw.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state.Messages[0].Content != original {
		t.Fatalf("off path mutated msgs[0]: want %q, got %q", original, state.Messages[0].Content)
	}
}

// Plan mode on → TodoInstruction is appended to msgs[0]; subsequent
// messages are untouched. Verifies the append happens in a clone (the
// outer slice is a new backing array) so a downstream middleware
// mutating ev.Messages can't accidentally rewrite the agent's prompt
// for the next turn.
func TestPlanReminder_OnAppendsAndPreservesTail(t *testing.T) {
	mw := NewPlanReminder(func() bool { return true })
	user := schema.UserMessage("hello")
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			schema.SystemMessage("you are helpful"),
			user,
		},
	}

	_, _, err := mw.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(state.Messages[0].Content, planModeTag) {
		t.Fatalf("expected planModeTag injected into msgs[0]; got %q", state.Messages[0].Content)
	}
	if !strings.HasPrefix(state.Messages[0].Content, "you are helpful") {
		t.Fatalf("original content lost: %q", state.Messages[0].Content)
	}
	if state.Messages[1] != user {
		t.Fatalf("tail message replaced; want pointer %p, got %p", user, state.Messages[1])
	}
}

// Calling twice in the same turn (e.g. retry after a transient model
// error) is safe — the tag check shortcuts the second pass. Without
// idempotency the prompt would grow by one TodoInstruction per retry.
func TestPlanReminder_Idempotent(t *testing.T) {
	mw := NewPlanReminder(func() bool { return true })
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{schema.SystemMessage("base")},
	}

	_, _, _ = mw.BeforeModelRewriteState(context.Background(), state, nil)
	first := state.Messages[0].Content
	_, _, _ = mw.BeforeModelRewriteState(context.Background(), state, nil)
	if state.Messages[0].Content != first {
		t.Fatalf("second pass mutated content; want idempotent. before=%q after=%q",
			first, state.Messages[0].Content)
	}
	if got := strings.Count(state.Messages[0].Content, planModeTag); got != 1 {
		t.Fatalf("expected exactly 1 planModeTag, got %d", got)
	}
}

// nil getOn / nil state are defensive — the eino agent always seeds a
// system message and we always wire getPlanMode in production, but
// tests / future call paths shouldn't panic.
func TestPlanReminder_NilSafetly(t *testing.T) {
	mw := NewPlanReminder(nil)
	_, _, err := mw.BeforeModelRewriteState(context.Background(), nil, nil)
	if err != nil {
		t.Fatalf("nil state should not error: %v", err)
	}

	mw = NewPlanReminder(func() bool { return true })
	state := &adk.ChatModelAgentState{Messages: nil}
	_, _, err = mw.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("empty messages should not error: %v", err)
	}
}
