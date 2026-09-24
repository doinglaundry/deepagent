package agentthread

import (
	"context"
	"eino-cli/deepagent/core/graph"
	"errors"
	"github.com/cloudwego/eino/schema"
	"testing"
	"time"
)

type pendingSaveStore struct {
	historyMemory
	started chan struct{}
	release chan struct{}
	failure error
}

func (s *pendingSaveStore) Append(ctx context.Context, r *HistoryRecord) error {
	if r.Message.Content == "accepted pending" {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if s.failure != nil {
			return s.failure
		}
	}
	return s.historyMemory.Append(ctx, r)
}
func TestThread_CancelPersistsAcceptedPendingBeforeFinalEventAndWait(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "save failure"}[fail], func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			failure := errors.New("history unavailable")
			store := &pendingSaveStore{started: make(chan struct{}), release: make(chan struct{})}
			if fail {
				store.failure = failure
			}
			model := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
			events := make(chan Event, 32)
			thread := New("thread", &RunConfig{Agent: graph.Config{Model: model}}, events, ThreadOptions{HistoryStore: store})
			if err := thread.Init(ctx); err != nil {
				t.Fatal(err)
			}
			first, err := thread.SubmitInput(runCtx, schema.UserMessage("first"))
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-model.started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			next, err := thread.SubmitInput(runCtx, schema.UserMessage("accepted pending"), WithInputMeta(map[string]string{"message_id": "pending-id"}))
			if err != nil || next.RunID != first.RunID {
				t.Fatalf("next=%+v err=%v", next, err)
			}
			cancel()
			select {
			case <-store.started:
			case <-ctx.Done():
				t.Fatal("accepted pending input never persisted")
			}
			if thread.ActiveRun() == nil {
				t.Fatal("run inactive before pending input persisted")
			}
			short, end := context.WithTimeout(ctx, 10*time.Millisecond)
			if err := first.RunHandle.Wait(short); !errors.Is(err, context.DeadlineExceeded) {
				end()
				t.Fatalf("Wait returned before persistence: %v", err)
			}
			end()
			for len(events) > 0 {
				if e := <-events; e.Type == EventRunEnd {
					t.Fatal("terminal event overtook history persistence")
				}
			}
			close(store.release)
			err = first.RunHandle.Wait(ctx)
			if !errors.Is(err, context.Canceled) || (fail && !errors.Is(err, failure)) {
				t.Fatalf("lost failure cause: %v", err)
			}
			history := thread.ContextManager().History(ctx)
			want := 2
			if fail {
				want = 1
			}
			if len(history) != want || len(store.records) != want {
				t.Fatalf("history=%v records=%d", history, len(store.records))
			}
			inputs := next.RunHandle.ConsumedInputs()
			if len(inputs) != 2 || inputs[1].Content != "accepted pending" || len(next.RunHandle.ConsumedInputsMeta()) != 2 {
				t.Fatal("accepted input ownership lost")
			}
			metadata, ok := next.RunHandle.ConsumedInputsMeta()[1].(map[string]string)
			if !ok || metadata["message_id"] != "pending-id" {
				t.Fatal("pending metadata changed")
			}
			if !fail && store.records[1].RunID != first.RunID {
				t.Fatal("pending history changed run ownership")
			}
			if err := thread.Close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
