package thread

import (
	"context"
	"eino-cli/deepagent/core/runtime/agentthread"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	corethread "eino-cli/deepagent/thread"
	"eino-cli/deepagent/worker/managed"
	"errors"
	"testing"
)

type routingThread struct {
	corethread.ThreadRuntime
	messages []*corethread.TransportMessage
	err      error
}

func (f *routingThread) PostMessage(_ context.Context, in *corethread.TransportMessage) (*corethread.TransportPostMessageResult, error) {
	f.messages = append(f.messages, in)
	if f.err != nil {
		return nil, f.err
	}
	return &corethread.TransportPostMessageResult{RunID: "old-run"}, nil
}
func TestAdapterValidatesAndRoutesResume(t *testing.T) {
	f := &routingThread{}
	r := &Runtime{source: f, thread: api.Thread{ID: "thread", SessionID: "session"}}
	if _, err := r.PostMessage(context.Background(), protocol.Input{Kind: protocol.InputResume}); err == nil {
		t.Fatal("invalid resume accepted")
	}
	id, err := r.PostMessage(context.Background(), protocol.Input{ID: "answer", Kind: protocol.InputResume, Resume: &protocol.Resume{Kind: "approval", RunID: "old-run", CheckpointID: "cp", InterruptID: "interrupt"}})
	if err != nil || id != "old-run" || len(f.messages) != 1 || f.messages[0].Type != corethread.MessageTypeResumeRun {
		t.Fatalf("messages=%v run=%s err=%v", f.messages, id, err)
	}
	if _, err := r.PostMessage(context.Background(), protocol.Input{ID: "wrong", Kind: protocol.InputUser, Text: "x", ThreadID: "other"}); err == nil {
		t.Fatal("wrong thread accepted")
	}
	if len(f.messages) != 1 {
		t.Fatal("invalid input reached Thread")
	}
}
func TestInputArrivingAtBlockBoundaryRemainsRetryable(t *testing.T) {
	for _, sourceErr := range []error{agentthread.ErrThreadRunning, agentthread.ErrThreadBackpressure} {
		r := &Runtime{source: &routingThread{err: sourceErr}, thread: api.Thread{ID: "thread"}}
		if _, err := r.PostMessage(context.Background(), protocol.Input{ID: "input", Kind: protocol.InputUser, Text: "next request"}); !errors.Is(err, managed.ErrBusy) {
			t.Fatalf("boundary rejected input permanently: %v", err)
		}
	}
	f := &routingThread{}
	r := &Runtime{source: f, thread: api.Thread{ID: "thread", Block: &protocol.Block{RunID: "blocked"}}}
	if _, err := r.PostMessage(context.Background(), protocol.Input{ID: "input", Kind: protocol.InputUser, Text: "next request"}); !errors.Is(err, managed.ErrBusy) {
		t.Fatal(err)
	}
	if len(f.messages) != 0 {
		t.Fatal("blocked Thread received input")
	}
}
