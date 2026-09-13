package tui

import (
	"context"
	legacy "eino-cli/backend/runtime"
	"eino-cli/protocol"
	"errors"
	"testing"
	"time"
)

func TestRemoteTextFinalReplacesDeltasAndToolsUpdateByCallID(t *testing.T) {
	m := &Model{toolBlocksEnabled: true, toolArgsMaxChars: 60}
	applyRemoteEvent(m, protocol.Event{Kind: protocol.EventTextDelta, ResponseID: "a", Text: "hel"})
	applyRemoteEvent(m, protocol.Event{Kind: protocol.EventText, ResponseID: "a", Text: "hello"})
	if m.streamBuf.String() != "hello" {
		t.Fatalf("delta/final duplicated: %q", m.streamBuf.String())
	}
	applyRemoteEvent(m, protocol.Event{Kind: protocol.EventToolStarted, ToolCallID: "one", ToolName: "read_file", Arguments: `{"path":"a"}`})
	applyRemoteEvent(m, protocol.Event{Kind: protocol.EventToolCompleted, ToolCallID: "one", ToolName: "read_file", Text: "contents"})
	if len(m.toolBlocks) != 1 || len(m.toolBlocks[0].lines) != 1 || m.toolBlocks[0].lines[0] != "contents" {
		t.Fatalf("tool call not reconciled: %#v", m.toolBlocks)
	}
}

// A failed cancel request must not silently detach from work still running remotely.
type cancelFailureRuntime struct {
	remoteRuntime
	started chan struct{}
	finish  chan struct{}
}

func (r *cancelFailureRuntime) ExecuteEvents(ctx context.Context, _ string, _ func(protocol.Event)) (legacy.Result, error) {
	close(r.started)
	select {
	case <-r.finish:
		return legacy.Result{Success: true, Output: "finished"}, nil
	case <-ctx.Done():
		return legacy.Result{}, ctx.Err()
	}
}
func (r *cancelFailureRuntime) Cancel(context.Context) error { return errors.New("unavailable") }
func TestFailedRemoteCancelKeepsSubscriptionAlive(t *testing.T) {
	r := &cancelFailureRuntime{started: make(chan struct{}), finish: make(chan struct{})}
	ch, cancel := startRemoteStream(r, "test")
	<-r.started
	cancel()
	select {
	case msg := <-ch:
		if _, ok := msg.(remoteCancelError); !ok {
			t.Fatalf("got %T", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel error not reported")
	}
	close(r.finish)
	select {
	case msg := <-ch:
		d, ok := msg.(doneMsg)
		if !ok || d.err != nil || d.output != "finished" {
			t.Fatalf("subscription detached: %+v", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("completion lost")
	}
}
func TestFinalRemoteTextIgnoresLatePubsubDeltas(t *testing.T) {
	m := &Model{}
	applyRemoteEvent(m, protocol.Event{Kind: protocol.EventText, ResponseID: "response", Text: "hello"})
	applyRemoteEvent(m, protocol.Event{Kind: protocol.EventTextDelta, ResponseID: "response", Text: "hel"})
	if m.streamBuf.String() != "hello" {
		t.Fatalf("late delta corrupted complete response: %q", m.streamBuf.String())
	}
}

func TestRemoteToolFinalIgnoresLateOutputAndSeparatesRuns(t *testing.T) {
	m := &Model{toolBlocksEnabled: true}
	applyRemoteEvent(m, protocol.Event{Kind: protocol.EventToolCompleted, RunID: "r1", ToolCallID: "call", Text: "complete"})
	applyRemoteEvent(m, protocol.Event{Kind: protocol.EventToolOutput, RunID: "r1", ToolCallID: "call", Text: "late"})
	if len(m.toolBlocks[0].lines) != 1 {
		t.Fatalf("late output appended to final result: %v", m.toolBlocks[0].lines)
	}
	applyRemoteEvent(m, protocol.Event{Kind: protocol.EventToolCompleted, RunID: "r2", ToolCallID: "call", Text: "second run"})
	if len(m.toolBlocks) != 2 {
		t.Fatal("different Runs reused one tool block")
	}
}
