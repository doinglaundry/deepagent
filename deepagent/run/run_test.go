package run

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"eino-cli/deepagent/graph/conversation"
	"eino-cli/deepagent/graph/execution"
	"eino-cli/deepagent/graph/middleware"
	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type lifecycleModel struct{}

func (*lifecycleModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return &lifecycleModel{}, nil
}
func (*lifecycleModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("answer", nil), nil
}
func (*lifecycleModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("answer", nil)}), nil
}

type lifecycleResource struct {
	middleware.BaseMiddleware
	mu     sync.Mutex
	closed int
	after  int
}

func (m *lifecycleResource) PrepareRun(context.Context, *agentmodel.RunState) error { return nil }
func (m *lifecycleResource) FinishRun(context.Context, *agentmodel.RunState, error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.after++
	return nil
}
func (m *lifecycleResource) Close(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed++
	return nil
}

func TestRun_WaitIncludesFinishAndResourceCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resource := &lifecycleResource{}
	finishing, release := make(chan struct{}), make(chan struct{})
	r, runCtx := New(ctx, "run", Config{
		Graph:  execution.Config{Model: &lifecycleModel{}, Middlewares: []agentmodel.Middleware{resource}},
		Events: make(chan agentmodel.RunEvent, 32),
		OnFinish: func(context.Context, *Run, error) error {
			resource.mu.Lock()
			closed, after := resource.closed, resource.after
			resource.mu.Unlock()
			if closed != 1 || after != 1 {
				t.Errorf("cleanup=%d after=%d", closed, after)
			}
			close(finishing)
			<-release
			return nil
		},
	})
	r.AddInputs(agentmodel.RunInput{MessageID: "input", Message: agentmodel.NewUserMessage("go")})
	go r.Execute(runCtx)
	select {
	case <-finishing:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !r.Handle().IsActive() {
		t.Fatal("Run became inactive before finalization")
	}
	waitCtx, waitCancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer waitCancel()
	waitErr := r.Handle().Wait(waitCtx)
	if !errors.Is(waitErr, context.DeadlineExceeded) {
		t.Fatalf("Wait returned before finalization: %v", waitErr)
	}
	close(release)
	err := r.Handle().Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.Handle().IsActive() {
		t.Fatal("completed Run remains active")
	}
}

func TestRun_PreStartCancelClosesConstructedMiddleware(t *testing.T) {
	resource := &lifecycleResource{}
	r, ctx := New(context.Background(), "run", Config{
		Graph:  execution.Config{Model: &lifecycleModel{}, Middlewares: []agentmodel.Middleware{resource}},
		Events: make(chan agentmodel.RunEvent, 32),
	})
	r.Cancel(context.Canceled)
	_, err := r.Execute(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
	resource.mu.Lock()
	defer resource.mu.Unlock()
	if resource.closed != 1 {
		t.Fatalf("resource closes=%d", resource.closed)
	}
}

type pausedAfterRun struct {
	middleware.BaseMiddleware
	entered chan struct{}
	release chan struct{}
}

func (m *pausedAfterRun) PrepareRun(context.Context, *agentmodel.RunState) error { return nil }
func (m *pausedAfterRun) FinishRun(context.Context, *agentmodel.RunState, error) error {
	close(m.entered)
	<-m.release
	return nil
}

func TestRun_InterruptDeadlineCoversCompletionHook(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	mw := &pausedAfterRun{entered: make(chan struct{}), release: make(chan struct{})}
	hookCause := make(chan error, 1)
	r, runCtx := New(ctx, "run", Config{
		Graph: execution.Config{
			Model: &lifecycleModel{}, Middlewares: []agentmodel.Middleware{mw},
			Conversation: conversation.New("thread", nil, nil, nil, 0, nil),
		},
		Events: make(chan agentmodel.RunEvent, 32),
		RunCompleted: func(ctx context.Context, _, _ string, _ model.ToolCallingChatModel, _ []*agentmodel.Message) {
			<-ctx.Done()
			hookCause <- context.Cause(ctx)
		},
	})
	go r.Execute(runCtx)
	select {
	case <-mw.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	timeout := 20 * time.Millisecond
	r.RequestInterrupt(agentmodel.InterruptOptions{Timeout: &timeout})
	close(mw.release)
	waitCtx, waitCancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer waitCancel()
	err := r.Wait(waitCtx)
	if err != nil {
		t.Fatalf("completion hook lost the interrupt deadline: %v", err)
	}
	cause := <-hookCause
	if cause != ErrExternalInterruptTimeout {
		t.Fatalf("completion cause=%v", cause)
	}
}
