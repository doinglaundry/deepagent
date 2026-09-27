package agentthread

import (
	"context"
	"eino-cli/deepagent/core/graph"
	"errors"
	"github.com/cloudwego/eino/schema"
	"testing"
	"time"
)

func TestThread_CancelDeliversFinalEventsBeforeInactive(t *testing.T) {
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
	events := make(chan Event)
	thread := New("thread", &RunConfig{Agent: graph.Config{Model: m}}, events, ThreadOptions{})
	accepted, err := thread.SubmitInput(runCtx, schema.UserMessage("go"))
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case e := <-events:
			if e.Type == EventLLMRequesting {
				goto modeling
			}
		case <-ctx.Done():
			t.Fatal("model request event missing")
		}
	}
modeling:
	select {
	case <-m.started:
	case <-ctx.Done():
		t.Fatal("model did not start")
	}
	cancelRun()
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	if err := accepted.RunHandle.Wait(short); !errors.Is(err, context.DeadlineExceeded) {
		stop()
		t.Fatalf("Wait bypassed terminal delivery: %v", err)
	}
	stop()
	if thread.ActiveRun() == nil {
		t.Fatal("run inactive before terminal delivery")
	}
	shortClose, stopClose := context.WithTimeout(ctx, 20*time.Millisecond)
	if err := thread.Close(shortClose); !errors.Is(err, context.DeadlineExceeded) {
		stopClose()
		t.Fatalf("Close bypassed terminal delivery: %v", err)
	}
	stopClose()
	for _, expected := range []EventType{EventError, EventRunEnd} {
		select {
		case e := <-events:
			if e.Type != expected || e.RunID != accepted.RunID {
				t.Fatalf("event=%+v expected=%s", e, expected)
			}
		case <-ctx.Done():
			t.Fatalf("missing terminal event %s", expected)
		}
	}
	if err := accepted.RunHandle.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation cause: %v", err)
	}
	if thread.ActiveRun() != nil {
		t.Fatal("run still active after delivery")
	}
	if err := thread.Close(ctx); err != nil {
		t.Fatalf("Close retry failed: %v", err)
	}
	select {
	case e := <-events:
		t.Fatalf("late event: %+v", e)
	default:
	}
}

func TestThread_CancellationKeepsHistoryAndThreadUsable(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
	th := New("thread", &RunConfig{Agent: graph.Config{Model: m}}, make(chan Event, 100), ThreadOptions{})
	first, err := th.SubmitInput(runCtx, schema.UserMessage("original"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	if err = first.RunHandle.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	second, err := th.SubmitInput(ctx, schema.UserMessage("again"))
	if err != nil {
		t.Fatal(err)
	}
	if err = second.RunHandle.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	if first.RunID == second.RunID || len(m.inputs) != 2 || m.inputs[1][0].Content != "original" || m.inputs[1][1].Content != "again" {
		t.Fatalf("cancel lost history or ownership: %v", m.inputs)
	}
}

func TestThread_CloseCancelsActiveRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
	thread := New("thread", &RunConfig{Agent: graph.Config{Model: m}}, make(chan Event, 16), ThreadOptions{})
	accepted, err := thread.SubmitInput(ctx, schema.UserMessage("run"))
	if err != nil {
		t.Fatal(err)
	}
	<-m.started
	if err := thread.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if thread.ActiveRun() != nil || accepted.RunHandle.IsActive() {
		t.Fatal("close returned with active run")
	}
	if _, err := thread.SubmitInput(ctx, schema.UserMessage("late")); err == nil {
		t.Fatal("closed thread accepted input")
	}
	if err := thread.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
