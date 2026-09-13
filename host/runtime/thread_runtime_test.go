package runtime

import (
	"context"
	"eino-cli/manager/api"
	"eino-cli/protocol"
	"testing"
	"time"
)

type immediateManager struct {
	api.Manager
	subscribed bool
	ch         chan protocol.Event
	rows       []protocol.Event
}

func (m *immediateManager) CreateThread(context.Context, api.CreateThreadRequest) (api.Thread, error) {
	return api.Thread{ID: "t", SessionID: "s"}, nil
}
func (m *immediateManager) SubscribeSession(context.Context, string) (*api.Subscription, error) {
	m.subscribed = true
	m.ch = make(chan protocol.Event, 8)
	return &api.Subscription{Events: m.ch, Close: func() {}}, nil
}
func (m *immediateManager) ListEvents(_ context.Context, f api.EventFilter) ([]protocol.Event, error) {
	var out []protocol.Event
	for _, e := range m.rows {
		if e.Sequence > f.After {
			out = append(out, e)
		}
	}
	return out, nil
}
func (m *immediateManager) SubmitInput(_ context.Context, _ string, in protocol.Input) (protocol.Input, error) {
	if !m.subscribed {
		panic("submitted before subscription")
	}
	in.ID = "message"
	m.rows = []protocol.Event{{ID: "start", Sequence: 1, ThreadID: "t", RunID: "r", MessageIDs: []string{in.ID}, Kind: protocol.EventRunStarted}, {ID: "text", Sequence: 2, ThreadID: "t", RunID: "r", Kind: protocol.EventText, Text: "hello"}, {ID: "done", Sequence: 3, ThreadID: "t", RunID: "r", Kind: protocol.EventRunCompleted}}
	// Duplicate delivery and a missing realtime event must still yield one ordered durable result.
	m.ch <- m.rows[0]
	m.ch <- m.rows[0]
	m.ch <- m.rows[2]
	return in, nil
}
func TestStartRunSubscribesBeforeFastWorkerAndReconcilesHistory(t *testing.T) {
	m := &immediateManager{}
	r := New(m, Config{SessionID: "s", WorkDir: t.TempDir(), PollInterval: time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	stream, err := r.StartRun(ctx, protocol.Input{Kind: protocol.InputUser, Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	seen := map[string]int{}
	for e := range stream.Events {
		seen[e.ID]++
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 3 || seen["start"] != 1 || seen["text"] != 1 || seen["done"] != 1 {
		t.Fatalf("lost or duplicate events: %v", seen)
	}
}
func TestClearDetachesWithoutClosingServerThread(t *testing.T) {
	m := &immediateManager{}
	r := New(m, Config{SessionID: "s", ThreadID: "old", WorkDir: t.TempDir()})
	r.ClearHistory()
	if r.ThreadID() != "" {
		t.Fatal("clear retained thread")
	}
}
