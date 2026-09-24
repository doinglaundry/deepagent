package graph

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/types"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type blockingResourceMiddleware struct {
	middleware.BaseMiddleware
	entered chan struct{}
	release chan struct{}
}

func (m *blockingResourceMiddleware) Close(context.Context) error {
	close(m.entered)
	<-m.release
	return nil
}

func TestClose_ConcurrentCallWaitsForUnstartedResourceCleanup(t *testing.T) {
	mw := &blockingResourceMiddleware{entered: make(chan struct{}), release: make(chan struct{})}
	a, err := New(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{mw}}))
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- a.Close(context.Background()) }()
	defer func() {
		close(mw.release)
		if err := <-first; err != nil {
			t.Error(err)
		}
	}()
	<-mw.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := a.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close returned before resource cleanup completed: %v", err)
	}
}

type resourceMiddleware struct {
	middleware.BaseMiddleware
	name                          string
	order                         *[]string
	toolsErr, beforeErr, closeErr error
	closed                        int
}

func (m *resourceMiddleware) Name() string { return m.name }
func (m *resourceMiddleware) Tools(context.Context) ([]einotool.BaseTool, error) {
	return nil, m.toolsErr
}
func (m *resourceMiddleware) BeforeRun(context.Context, *types.RunState) error       { return m.beforeErr }
func (m *resourceMiddleware) AfterRun(context.Context, *types.RunState, error) error { return nil }
func (m *resourceMiddleware) Close(ctx context.Context) error {
	m.closed++
	if m.order != nil {
		*m.order = append(*m.order, m.name)
	}
	if ctx.Err() != nil {
		return errors.New("cleanup received canceled context")
	}
	return m.closeErr
}

func TestNew_ConstructionFailureClosesResourcesInReverseOrder(t *testing.T) {
	want, closeErr := errors.New("tool construction failed"), errors.New("resource close failed")
	var order []string
	first := &resourceMiddleware{name: "first", order: &order, closeErr: closeErr}
	second := &resourceMiddleware{name: "second", order: &order, toolsErr: want}
	a, err := New(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{first, second}}))
	if a != nil || !errors.Is(err, want) || !errors.Is(err, closeErr) {
		t.Fatalf("agent=%v err=%v", a, err)
	}
	if !reflect.DeepEqual(order, []string{"second", "first"}) || first.closed != 1 || second.closed != 1 {
		t.Fatalf("order=%v", order)
	}
}

func TestClose_UnstartedAgentClosesConstructedResourcesOnce(t *testing.T) {
	mw := &resourceMiddleware{name: "resource"}
	a, err := New(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{mw}}))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := a.Close(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if mw.closed != 1 {
		t.Fatalf("closed=%d", mw.closed)
	}
}

func TestRun_FailedStartClosesResourcesAndPreservesBothErrors(t *testing.T) {
	want, closeErr := errors.New("before run failed"), errors.New("close failed")
	mw := &resourceMiddleware{name: "resource", beforeErr: want, closeErr: closeErr}
	a, err := New(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{mw}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if !errors.Is(err, want) || !errors.Is(err, closeErr) || mw.closed != 1 {
		t.Fatalf("err=%v closed=%d", err, mw.closed)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mw.closed != 1 {
		t.Fatalf("double close: %d", mw.closed)
	}
}
