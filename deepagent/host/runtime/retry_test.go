package runtime

import (
	"context"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	"testing"
	"time"
)

type retryManager struct{ immediateManager }

func (m *retryManager) SubmitInput(_ context.Context, _ string, in protocol.Input) (protocol.Input, error) {
	in.ID = "input"
	m.rows = []protocol.Event{
		{ID: "s1", Sequence: 1, ThreadID: "t", RunID: "r1", MessageIDs: []string{in.ID}, Kind: protocol.EventRunStarted},
		{ID: "s2", Sequence: 2, ThreadID: "t", RunID: "r2", MessageIDs: []string{in.ID}, Kind: protocol.EventRunStarted},
		{ID: "old", Sequence: 3, ThreadID: "t", RunID: "r1", Kind: protocol.EventRunCompleted},
		{ID: "answer", Sequence: 4, ThreadID: "t", RunID: "r2", Kind: protocol.EventText, Text: "retried"},
		{ID: "end", Sequence: 5, ThreadID: "t", RunID: "r2", Kind: protocol.EventRunCompleted},
	}
	return in, nil
}
func TestFollowRetryRunAndIgnoreStaleRunTerminal(t *testing.T) {
	m := &retryManager{}
	r := New(m, Config{SessionID: "s", PollInterval: time.Millisecond})
	defer r.Detach()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := r.StartRun(ctx, protocol.Input{Kind: protocol.InputUser, Text: "test"})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for e := range stream.Events {
		seen[e.ID] = true
	}
	if seen["old"] || !seen["answer"] || !seen["end"] {
		t.Fatalf("bad retry correlation: %v", seen)
	}
}

var _ api.Manager = (*retryManager)(nil)

type compactManager struct{ immediateManager }

func (m *compactManager) SubmitInput(_ context.Context, _ string, in protocol.Input) (protocol.Input, error) {
	in.ID = "compact-input"
	m.rows = []protocol.Event{{ID: "compact-event", Sequence: 1, ThreadID: "t", MessageIDs: []string{in.ID}, Kind: protocol.EventCompacted, Text: "Context compacted"}}
	return in, nil
}
func TestCompactOperationCompletesWithoutInventingRun(t *testing.T) {
	r := New(&compactManager{}, Config{SessionID: "s", PollInterval: time.Millisecond})
	defer r.Detach()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	stream, err := r.StartRun(ctx, protocol.Input{Kind: protocol.InputCompact})
	if err != nil {
		t.Fatal(err)
	}
	var got []protocol.Event
	for e := range stream.Events {
		got = append(got, e)
	}
	if err = stream.Err(); err != nil || len(got) != 1 || got[0].Kind != protocol.EventCompacted {
		t.Fatalf("compact not completed: %v %v", got, err)
	}
}

func TestStreamCorrelationRejectsStaleLiveDeltaAndLateFullResponseDelta(t *testing.T) {
	track := newEventTracker("input", "", protocol.InputUser)
	if !track.accept(protocol.Event{ID: "s1", ThreadID: "t", RunID: "r1", MessageIDs: []string{"input"}, Kind: protocol.EventRunStarted}) {
		t.Fatal("first run not bound")
	}
	if !track.accept(protocol.Event{ID: "s2", RunID: "r2", MessageIDs: []string{"input"}, Kind: protocol.EventRunStarted}) {
		t.Fatal("retry run not bound")
	}
	if track.accept(protocol.Event{ID: "old-live", RunID: "r1", MessageIDs: []string{"input"}, Kind: protocol.EventTextDelta, ResponseID: "old", Text: "old"}) {
		t.Fatal("stale live event rebound run")
	}
	if !track.accept(protocol.Event{ID: "full", RunID: "r2", Kind: protocol.EventText, ResponseID: "a", Text: "hello"}) {
		t.Fatal("full response rejected")
	}
	if track.accept(protocol.Event{ID: "late", RunID: "r2", Kind: protocol.EventTextDelta, ResponseID: "a", Text: "hel"}) {
		t.Fatal("late delta accepted")
	}
	if !track.accept(protocol.Event{ID: "done", RunID: "r2", Kind: protocol.EventRunCompleted}) {
		t.Fatal("retry completion rejected")
	}
}

type failedResumeManager struct {
	immediateManager
	blocked bool
	submits []protocol.InputKind
}

func (m *failedResumeManager) GetThread(context.Context, string) (api.Thread, error) {
	s := api.Idle
	if m.blocked {
		s = api.Blocked
	}
	return api.Thread{ID: "t", SessionID: "s", State: s, Block: &protocol.Block{RunID: "r", CheckpointID: "c", InterruptID: "i"}}, nil
}
func (m *failedResumeManager) ResumeFromBlock(_ context.Context, _ string, in protocol.Input) (protocol.Input, error) {
	m.submits = append(m.submits, in.Kind)
	m.blocked = false
	m.rows = append(m.rows, protocol.Event{ID: "failure", Sequence: 1, ThreadID: "t", RunID: "r", MessageIDs: []string{in.ID}, Kind: protocol.EventRunFailed, Error: "tool failed"})
	return in, nil
}
func (m *failedResumeManager) SubmitInput(_ context.Context, _ string, in protocol.Input) (protocol.Input, error) {
	m.submits = append(m.submits, in.Kind)
	m.rows = append(m.rows, protocol.Event{ID: "new-done", Sequence: 2, ThreadID: "t", RunID: "r2", MessageIDs: []string{in.ID}, Kind: protocol.EventRunCompleted})
	return in, nil
}
func TestAcceptedResumeDoesNotLeakPendingBlockIntoNextRun(t *testing.T) {
	m := &failedResumeManager{blocked: true}
	r := New(m, Config{ThreadID: "t", SessionID: "s", PollInterval: time.Millisecond})
	defer r.Detach()
	r.pending = &protocol.Block{RunID: "r", CheckpointID: "c", InterruptID: "i"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := r.ExecuteEvents(ctx, "yes", nil); err == nil {
		t.Fatal("expected failed resumed execution")
	}
	result, err := r.ExecuteEvents(ctx, "new request", nil)
	if err != nil || !result.Success {
		t.Fatalf("new request used stale block: %v %+v", err, result)
	}
	if len(m.submits) != 2 || m.submits[1] != protocol.InputUser {
		t.Fatalf("submitted %v", m.submits)
	}
}

type compactFailureManager struct{ immediateManager }

func (m *compactFailureManager) SubmitInput(_ context.Context, _ string, in protocol.Input) (protocol.Input, error) {
	in.ID = "compact-fail-input"
	m.rows = []protocol.Event{{ID: "failed", Sequence: 1, ThreadID: "t", RunID: "failed-run", Kind: protocol.EventRunFailed, MessageIDs: []string{in.ID}, Error: "summary unavailable"}}
	return in, nil
}
func TestCompactDoesNotReportSuccessOnWorkerFailure(t *testing.T) {
	r := New(&compactFailureManager{}, Config{SessionID: "s", PollInterval: time.Millisecond})
	defer r.Detach()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := r.Compact(ctx)
	if err == nil || result.Success {
		t.Fatalf("reported successful compaction: %+v %v", result, err)
	}
}
func TestCutoffCancellationDoesNotEndNewerRequestInSameRun(t *testing.T) {
	track := newEventTracker("new", "", protocol.InputUser)
	track.accept(protocol.Event{ID: "consumed", Kind: protocol.EventInputConsumed, RunID: "shared", MessageIDs: []string{"new"}})
	if track.accept(protocol.Event{ID: "cutoff", Kind: protocol.EventRunCancelled, RunID: "shared", MessageIDs: []string{"old"}}) {
		t.Fatal("older cutoff cancellation ended newer request")
	}
	if !track.accept(protocol.Event{ID: "retry", Kind: protocol.EventInputConsumed, RunID: "retry-run", MessageIDs: []string{"new"}}) {
		t.Fatal("newer input retry not followed")
	}
}
