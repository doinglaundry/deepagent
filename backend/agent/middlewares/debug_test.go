package middlewares

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

const testAgentName = "test-agent"

// recordingConsumer collects DebugEvents for assertions (single-goroutine).
type recordingConsumer struct {
	events []DebugEvent
}

func (c *recordingConsumer) Send(ev DebugEvent) {
	c.events = append(c.events, ev)
}

func makeMessages(n int) []*schema.Message {
	msgs := make([]*schema.Message, n)
	for i := range msgs {
		msgs[i] = schema.UserMessage("m")
	}
	return msgs
}

func TestTrace_NoConsumerIsNoop(t *testing.T) {
	tr := NewTrace(testAgentName)
	state := &adk.ChatModelAgentState{Messages: makeMessages(3)}

	gotCtx, gotState, err := tr.BeforeModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("BeforeModelRewriteState returned error: %v", err)
	}
	if gotCtx == nil || gotState != state {
		t.Fatalf("Before: ctx/state should pass through unchanged")
	}

	gotCtx, gotState, err = tr.AfterModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatalf("AfterModelRewriteState returned error: %v", err)
	}
	if gotCtx == nil || gotState != state {
		t.Fatalf("After: ctx/state should pass through unchanged")
	}
}

func TestTrace_SendsBeforeAndAfter(t *testing.T) {
	tr := NewTrace(testAgentName)
	rec := &recordingConsumer{}
	ctx := WithDebugConsumer(context.Background(), rec)
	state := &adk.ChatModelAgentState{Messages: makeMessages(3)}

	if _, _, err := tr.BeforeModelRewriteState(ctx, state, nil); err != nil {
		t.Fatalf("Before: %v", err)
	}
	if _, _, err := tr.AfterModelRewriteState(ctx, state, nil); err != nil {
		t.Fatalf("After: %v", err)
	}

	if got := len(rec.events); got != 2 {
		t.Fatalf("expected 2 events, got %d", got)
	}

	before := rec.events[0]
	if before.AgentName != testAgentName {
		t.Errorf("Before: AgentName = %q, want %q", before.AgentName, testAgentName)
	}
	if before.Phase != DebugBefore {
		t.Errorf("Before: Phase = %d, want DebugBefore (%d)", before.Phase, DebugBefore)
	}
	if before.Turn != 1 {
		t.Errorf("Before: Turn = %d, want 1", before.Turn)
	}
	if len(before.Messages) != 3 {
		t.Errorf("Before: len(Messages) = %d, want 3", len(before.Messages))
	}
	state.Messages = append(state.Messages, schema.UserMessage("extra"))
	if len(before.Messages) != 3 {
		t.Errorf("Before: snapshot aliased to state.Messages; len now %d", len(before.Messages))
	}

	after := rec.events[1]
	if after.AgentName != testAgentName {
		t.Errorf("After: AgentName = %q, want %q", after.AgentName, testAgentName)
	}
	if after.Phase != DebugAfter {
		t.Errorf("After: Phase = %d, want DebugAfter (%d)", after.Phase, DebugAfter)
	}
	if after.Turn != 1 {
		t.Errorf("After: Turn = %d, want 1", after.Turn)
	}
	if len(after.Messages) != 1 {
		t.Errorf("After: len(Messages) = %d, want 1", len(after.Messages))
	}
}

func TestTrace_TurnMonotonic(t *testing.T) {
	tr := NewTrace(testAgentName)
	rec := &recordingConsumer{}
	ctx := WithDebugConsumer(context.Background(), rec)
	state := &adk.ChatModelAgentState{Messages: makeMessages(1)}

	for i := 0; i < 2; i++ {
		if _, _, err := tr.BeforeModelRewriteState(ctx, state, nil); err != nil {
			t.Fatalf("Before #%d: %v", i, err)
		}
		if _, _, err := tr.AfterModelRewriteState(ctx, state, nil); err != nil {
			t.Fatalf("After #%d: %v", i, err)
		}
	}

	wantTurns := []int{1, 1, 2, 2}
	if got := len(rec.events); got != len(wantTurns) {
		t.Fatalf("expected %d events, got %d", len(wantTurns), got)
	}
	for i, want := range wantTurns {
		if got := rec.events[i].Turn; got != want {
			t.Errorf("event[%d].Turn = %d, want %d", i, got, want)
		}
	}
}

func TestTrace_ResetTurn(t *testing.T) {
	tr := NewTrace(testAgentName)
	rec := &recordingConsumer{}
	ctx := WithDebugConsumer(context.Background(), rec)
	state := &adk.ChatModelAgentState{Messages: makeMessages(1)}

	if _, _, err := tr.BeforeModelRewriteState(ctx, state, nil); err != nil {
		t.Fatalf("Before #1: %v", err)
	}
	if _, _, err := tr.AfterModelRewriteState(ctx, state, nil); err != nil {
		t.Fatalf("After #1: %v", err)
	}

	tr.ResetTurn()

	if _, _, err := tr.BeforeModelRewriteState(ctx, state, nil); err != nil {
		t.Fatalf("Before #2: %v", err)
	}
	if _, _, err := tr.AfterModelRewriteState(ctx, state, nil); err != nil {
		t.Fatalf("After #2: %v", err)
	}

	wantTurns := []int{1, 1, 1, 1}
	if got := len(rec.events); got != len(wantTurns) {
		t.Fatalf("expected %d events, got %d", len(wantTurns), got)
	}
	for i, want := range wantTurns {
		if got := rec.events[i].Turn; got != want {
			t.Errorf("event[%d].Turn = %d, want %d (post-reset turn should restart at 1)", i, got, want)
		}
	}
}

func TestFindTrace(t *testing.T) {
	tr := NewTrace(testAgentName)

	if got := FindTrace([]adk.ChatModelAgentMiddleware{tr}); got != tr {
		t.Errorf("single-element list: got %v, want %v", got, tr)
	}
	if got := FindTrace([]adk.ChatModelAgentMiddleware{NewClarification(), tr}); got != tr {
		t.Errorf("trace at end: got %v, want %v", got, tr)
	}
	if got := FindTrace([]adk.ChatModelAgentMiddleware{NewClarification()}); got != nil {
		t.Errorf("no trace in list: got %v, want nil", got)
	}
	if got := FindTrace(nil); got != nil {
		t.Errorf("nil list: got %v, want nil", got)
	}
}
