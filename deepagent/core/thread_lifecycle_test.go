package deepagents

import (
	"context"
	"eino-cli/deepagent/core/middleware"
	"errors"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

// A finished execution must leave the same Thread ready for another Run,
// while retaining the history consumed by the next model request.
func TestThreadOwnsSuccessiveRunsAndHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := &publicModel{}
	thread, err := NewThread(ThreadConfig{
		ThreadID:  "thread",
		RunConfig: &RunConfig{Agent: Config{Model: m}},
		Events:    make(chan Event, 128),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer thread.Close(ctx)
	err = thread.InitHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := thread.SubmitInput(ctx, schema.UserMessage("first"), WithMessageID("1"))
	if err != nil {
		t.Fatal(err)
	}
	err = first.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := thread.SubmitInput(ctx, schema.UserMessage("second"), WithMessageID("2"))
	if err != nil {
		t.Fatal(err)
	}
	err = second.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID == second.RunID || !second.Started || thread.CurrentRun() != nil {
		t.Fatal("completed execution did not release the Thread for a distinct Run")
	}
	if len(m.inputs) != 2 || len(m.inputs[1]) != 3 || m.inputs[1][0].Content != "first" || m.inputs[1][2].Content != "second" {
		t.Fatalf("next Run lost Thread history: %+v", m.inputs)
	}
	repeated, err := thread.SubmitInput(ctx, schema.UserMessage("first"), WithMessageID("1"))
	if err != nil {
		t.Fatal(err)
	}
	if repeated.RunID != first.RunID || repeated.Started || len(m.inputs) != 2 {
		t.Fatal("redelivery started a new execution instead of returning original ownership")
	}
}

// Closing a Thread must not close its filesystem while model work is still
// outstanding, even when the first Close caller times out.
func TestThreadCloseWaitsBeforeClosingResources(t *testing.T) {
	ctx := context.Background()
	m := &threadCloseModel{started: make(chan struct{}), release: make(chan struct{})}
	closed := 0
	thread, err := NewThread(ThreadConfig{
		ThreadID:       "thread",
		RunConfig:      &RunConfig{Agent: Config{Model: m}},
		CloseResources: func(context.Context) error { closed++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := thread.SubmitInput(ctx, schema.UserMessage("wait"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.started:
	case <-time.After(time.Second):
		t.Fatal("model did not start")
	}
	closeCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	err = thread.Close(closeCtx)
	if !errors.Is(err, context.DeadlineExceeded) || closed != 0 {
		t.Fatalf("closed resources before execution settled: error=%v closes=%d", err, closed)
	}
	close(m.release)
	waitCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	_ = accepted.RunHandle.Wait(waitCtx)
	err = thread.Close(waitCtx)
	if err != nil || closed != 1 {
		t.Fatalf("cleanup error=%v closes=%d", err, closed)
	}
	err = thread.Close(waitCtx)
	if err != nil || closed != 1 {
		t.Fatalf("duplicate cleanup error=%v closes=%d", err, closed)
	}
}

type threadCloseModel struct{ started, release chan struct{} }

func (m *threadCloseModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (*threadCloseModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	panic("unexpected Generate")
}
func (m *threadCloseModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	close(m.started)
	<-m.release
	return nil, ctx.Err()
}

func TestThreadCancellationBeforeGraphClosesMiddleware(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	mw := &threadCloseMiddleware{ready: make(chan struct{})}
	events := make(chan Event)
	thread := newTestThread("thread", &RunConfig{Agent: Config{Model: &publicModel{}, Middlewares: []middleware.Middleware{mw}}}, events, ThreadOptions{})
	accepted, err := thread.SubmitInput(runCtx, schema.UserMessage("go"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-mw.ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// RunStart cannot be delivered on this unbuffered channel before cancellation.
	cancel()
draining:
	for {
		select {
		case event := <-events:
			if event.Type == EventRunEnd {
				break draining
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	err = accepted.RunHandle.Wait(ctx)
	if !errors.Is(err, context.Canceled) || mw.closed.Load() != 1 {
		t.Fatalf("early cancellation leaked middleware: error=%v closes=%d", err, mw.closed.Load())
	}
}

type threadCloseMiddleware struct {
	middleware.BaseMiddleware
	ready  chan struct{}
	closed atomic.Int32
}

func (*threadCloseMiddleware) Name() string { return "thread_close" }
func (m *threadCloseMiddleware) Tools(context.Context) ([]tool.BaseTool, error) {
	close(m.ready)
	return nil, nil
}
func (m *threadCloseMiddleware) Close(context.Context) error { m.closed.Add(1); return nil }
