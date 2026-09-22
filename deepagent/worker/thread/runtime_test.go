package thread

import (
	"context"
	"eino-cli/deepagent/core/engine/agentthread"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	"eino-cli/deepagent/worker/managed"
	"errors"
	"testing"
)

type fakeEngine struct {
	calls  []string
	events chan protocol.Event
}

func (f *fakeEngine) SubmitInput(context.Context, protocol.Input) (string, error) {
	f.calls = append(f.calls, "submit")
	return "run", nil
}
func (f *fakeEngine) ResumeRun(context.Context, protocol.Input) (string, error) {
	f.calls = append(f.calls, "resume")
	return "old-run", nil
}
func (f *fakeEngine) Cancel()                       { f.calls = append(f.calls, "cancel") }
func (f *fakeEngine) Events() <-chan protocol.Event { return f.events }
func (f *fakeEngine) Close() error                  { close(f.events); return nil }
func TestAdapterValidatesAndRoutesResume(t *testing.T) {
	f := &fakeEngine{events: make(chan protocol.Event)}
	r := New(f, api.Thread{ID: "thread", SessionID: "session"})
	if _, err := r.PostMessage(context.Background(), protocol.Input{Kind: protocol.InputResume}); err == nil {
		t.Fatal("invalid resume accepted")
	}
	id, err := r.PostMessage(context.Background(), protocol.Input{Kind: protocol.InputResume, Resume: &protocol.Resume{RunID: "old-run", CheckpointID: "cp", InterruptID: "interrupt"}})
	if err != nil || id != "old-run" || len(f.calls) != 1 || f.calls[0] != "resume" {
		t.Fatalf("calls=%v run=%s err=%v", f.calls, id, err)
	}
	if _, err = r.PostMessage(context.Background(), protocol.Input{Kind: protocol.InputUser, Text: "x", ThreadID: "other"}); err == nil {
		t.Fatal("wrong thread accepted")
	}
}

type blockedEngine struct{ fakeEngine }

func (*blockedEngine) SubmitInput(context.Context, protocol.Input) (string, error) {
	return "", agentthread.ErrBlocked
}
func TestInputArrivingAtBlockBoundaryRemainsRetryable(t *testing.T) {
	r := New(&blockedEngine{}, api.Thread{ID: "thread"})
	if _, err := r.PostMessage(context.Background(), protocol.Input{Kind: protocol.InputUser, Text: "next request"}); !errors.Is(err, managed.ErrBusy) {
		t.Fatalf("block boundary rejected pending input permanently: %v", err)
	}
}
