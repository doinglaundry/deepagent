package middlewares

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/schema"
)

const testAgentName = "test-agent"

// recordingConsumer collects TraceEvents for assertions (single-goroutine).
type recordingConsumer struct {
	events []TraceEvent
}

func (c *recordingConsumer) Send(ev TraceEvent) {
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
	ctx := WithTraceConsumer(context.Background(), rec)
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
	if before.Phase != TracePhaseBefore {
		t.Errorf("Before: Phase = %d, want TracePhaseBefore (%d)", before.Phase, TracePhaseBefore)
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
	if after.Phase != TracePhaseAfter {
		t.Errorf("After: Phase = %d, want TracePhaseAfter (%d)", after.Phase, TracePhaseAfter)
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
	ctx := WithTraceConsumer(context.Background(), rec)
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
	ctx := WithTraceConsumer(context.Background(), rec)
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

// TracePhaseTodos must be a distinct phase value so consumers can switch
// on it without colliding with Before / After. AfterModelRewriteState
// only emits it when adk session has SessionKeyTodos with a non-empty
// slice; the no-session ctx path is already covered by
// TestTrace_SendsBeforeAndAfter (which asserts exactly 2 events).
func TestTraceEvent_TodosFieldRoundtrip(t *testing.T) {
	if TracePhaseBefore == TracePhaseTodos || TracePhaseAfter == TracePhaseTodos {
		t.Fatalf("TracePhaseTodos must be distinct from Before/After (%d vs %d/%d)",
			TracePhaseTodos, TracePhaseBefore, TracePhaseAfter)
	}
	ev := TraceEvent{
		Phase: TracePhaseTodos,
		Todos: []deep.TODO{{Content: "x", Status: "pending"}},
	}
	if ev.Phase != TracePhaseTodos || len(ev.Todos) != 1 {
		t.Fatalf("TraceEvent.Todos not round-tripping: %+v", ev)
	}
}

// TokenSnapshot, when wired, fires a separate TracePhaseTokens event on
// every After hook. Pointer-only Tokens field stays nil for the other
// phases (Before / After / Todos), confirming the by-value channel send
// stays cheap for non-token events.
func TestTrace_EmitsTokensPhaseWhenSnapshotWired(t *testing.T) {
	tr := NewTrace(testAgentName)
	tr.TokenSnapshot = func() TokenUsageStats {
		return TokenUsageStats{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, Calls: 1}
	}
	rec := &recordingConsumer{}
	ctx := WithTraceConsumer(context.Background(), rec)
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.AssistantMessage("hi", nil)}}

	if _, _, err := tr.AfterModelRewriteState(ctx, state, nil); err != nil {
		t.Fatalf("After: %v", err)
	}

	var tokens *TraceEvent
	for i := range rec.events {
		if rec.events[i].Phase == TracePhaseTokens {
			tokens = &rec.events[i]
		}
	}
	if tokens == nil {
		t.Fatalf("expected TracePhaseTokens event, got phases: %v", phaseList(rec.events))
	}
	if tokens.Tokens == nil || tokens.Tokens.TotalTokens != 150 {
		t.Fatalf("Tokens.TotalTokens = %v, want 150", tokens.Tokens)
	}
}

// nil snapshot -> no TracePhaseTokens event. Test stubs can leave
// TokenSnapshot zero-valued.
func TestTrace_TokensPhaseSkippedWithoutSnapshot(t *testing.T) {
	tr := NewTrace(testAgentName)
	rec := &recordingConsumer{}
	ctx := WithTraceConsumer(context.Background(), rec)
	state := &adk.ChatModelAgentState{Messages: []*schema.Message{schema.AssistantMessage("hi", nil)}}

	if _, _, err := tr.AfterModelRewriteState(ctx, state, nil); err != nil {
		t.Fatalf("After: %v", err)
	}
	for _, ev := range rec.events {
		if ev.Phase == TracePhaseTokens {
			t.Fatalf("unexpected TracePhaseTokens event with nil snapshot: %+v", ev)
		}
	}
}

func phaseList(events []TraceEvent) []int {
	out := make([]int, len(events))
	for i, ev := range events {
		out[i] = ev.Phase
	}
	return out
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
