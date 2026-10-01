package deepagents

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type overlappingRunModel struct {
	ready   chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (m *overlappingRunModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (*overlappingRunModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("unexpected non-stream model call")
}
func (m *overlappingRunModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls.Add(1)
	for _, message := range input {
		if message.Role == schema.Tool {
			return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
		}
	}
	select {
	case m.ready <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-m.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	call := schema.ToolCall{ID: "same-call", Type: "function", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{call})}), nil
}

func TestRun_ConcurrentRunsDoNotShareState(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	guard := middleware.NewLoopGuard()
	guard.HardLimit = 2
	m := &overlappingRunModel{ready: make(chan struct{}, 2), release: make(chan struct{})}
	tool := &countingTool{}
	cfg := &Config{Model: m, Middlewares: []middleware.Middleware{guard}, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool}}}
	agents := make([]*Run, 2)
	for i := range agents {
		var err error
		agents[i], err = NewRun(ctx, WithConfig(cfg))
		if err != nil {
			t.Fatal(err)
		}
		defer agents[i].Close(context.Background())
	}
	if agents[0].middlewares[0] == agents[1].middlewares[0] {
		t.Fatal("runs share mutable LoopGuard")
	}
	results := make(chan error, 2)
	for _, agent := range agents {
		go func(a *Run) {
			out, err := a.Execute(ctx, []*schema.Message{schema.UserMessage("go")})
			if err == nil && (out == nil || out.Content != "done") {
				err = fmt.Errorf("unexpected output: %v", out)
			}
			results <- err
		}(agent)
	}
	for range agents {
		select {
		case <-m.ready:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(m.release)
	for range agents {
		{
			err := <-results
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	{
		got := tool.count.Load()
		if got != 2 {
			t.Fatalf("shared middleware suppressed a tool call: %d", got)
		}
	}
}

func TestRun_SecondRunRejectedWithoutSideEffects(t *testing.T) {
	ctx := context.Background()
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}
	mw := &resourceMiddleware{name: "resource"}
	a, err := NewRun(ctx, WithConfig(&Config{Model: m, Middlewares: []middleware.Middleware{mw}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Execute(ctx, []*schema.Message{schema.UserMessage("first")})
	if err != nil {
		t.Fatal(err)
	}
	state := a.state
	history := a.conversation.History(ctx)
	optionsCalled := false
	out, err := a.Execute(ctx, []*schema.Message{schema.UserMessage("second")}, func(*RunOptions) { optionsCalled = true })
	if err == nil || out != nil || optionsCalled || m.calls != 1 || mw.closed != 1 {
		t.Fatalf("out=%v err=%v options=%v model=%d closed=%d", out, err, optionsCalled, m.calls, mw.closed)
	}
	if a.state != state || !reflect.DeepEqual(history, a.conversation.History(ctx)) {
		t.Fatal("rejected run changed state or history")
	}
}

func TestRun_NewAgentsShareCallerOwnedFilesystem(t *testing.T) {
	m := &sequenceModel{}
	for range 2 {
		m.responses = append(m.responses,
			[]*schema.Message{schema.AssistantMessage("", []schema.ToolCall{{ID: "command", Function: schema.FunctionCall{Name: "execute", Arguments: `{"command":"pwd"}`}}})},
			[]*schema.Message{schema.AssistantMessage("done", nil)})
	}
	root := t.TempDir()
	filesystem, err := backend.NewLocalFilesystem(&backend.LocalFilesystemConfig{RootDir: root, VirtualMode: true}, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close(context.Background())
	for i := 0; i < 2; i++ {
		a, err := NewRun(context.Background(), WithConfig(&Config{Model: m, Filesystem: filesystem, FilesystemConfig: &FilesystemConfig{DisableApplyPatch: true}, Policy: tools.PolicyFunc(func(context.Context, types.ToolCall, tools.ToolDescriptor) (tools.Decision, error) {
			return tools.Decision{Action: tools.Allow}, nil
		})}))
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.Execute(context.Background(), []*schema.Message{schema.UserMessage("where")})
		if err != nil {
			t.Fatal(err)
		}
		history := a.conversation.History(context.Background())
		result := history[len(history)-2]
		if result.Role != schema.Tool || !strings.Contains(result.Content, "exit_code=0") || !strings.Contains(result.Content, root) {
			t.Fatalf("run=%d result=%+v", i, result)
		}
		err = a.Close(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
}

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
	a, err := NewRun(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{mw}}))
	if err != nil {
		t.Fatal(err)
	}
	first := make(chan error, 1)
	go func() { first <- a.Close(context.Background()) }()
	defer func() {
		close(mw.release)
		{
			err := <-first
			if err != nil {
				t.Error(err)
			}
		}
	}()
	<-mw.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	{
		err := a.Close(ctx)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Close returned before resource cleanup completed: %v", err)
		}
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
	a, err := NewRun(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{first, second}}))
	if a != nil || !errors.Is(err, want) || !errors.Is(err, closeErr) {
		t.Fatalf("agent=%v err=%v", a, err)
	}
	if !reflect.DeepEqual(order, []string{"second", "first"}) || first.closed != 1 || second.closed != 1 {
		t.Fatalf("order=%v", order)
	}
}

func TestClose_UnstartedAgentClosesConstructedResourcesOnce(t *testing.T) {
	mw := &resourceMiddleware{name: "resource"}
	a, err := NewRun(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{mw}}))
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		{
			err := a.Close(context.Background())
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if mw.closed != 1 {
		t.Fatalf("closed=%d", mw.closed)
	}
}

func TestRun_FailedStartClosesResourcesAndPreservesBothErrors(t *testing.T) {
	want, closeErr := errors.New("before run failed"), errors.New("close failed")
	mw := &resourceMiddleware{name: "resource", beforeErr: want, closeErr: closeErr}
	a, err := NewRun(context.Background(), WithConfig(&Config{Model: &sequenceModel{}, Middlewares: []middleware.Middleware{mw}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Execute(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if !errors.Is(err, want) || !errors.Is(err, closeErr) || mw.closed != 1 {
		t.Fatalf("err=%v closed=%d", err, mw.closed)
	}
	_, err = a.Execute(context.Background(), nil)
	if err == nil || mw.closed != 1 {
		t.Fatalf("failed run allowed reuse: err=%v closed=%d", err, mw.closed)
	}
	err = a.Close(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mw.closed != 1 {
		t.Fatalf("double close: %d", mw.closed)
	}
}

// Checkpoint reads must not hold the Agent mutex: Close needs it to cancel.
type cancelableCheckpointRead struct {
	entered chan struct{}
}

func (s *cancelableCheckpointRead) Get(ctx context.Context, _ string) ([]byte, bool, error) {
	close(s.entered)
	<-ctx.Done()
	return nil, false, ctx.Err()
}

func (*cancelableCheckpointRead) Set(context.Context, string, []byte) error {
	return nil
}

func TestRun_CloseCancelsCheckpointRead(t *testing.T) {
	ctx := context.Background()
	store := &cancelableCheckpointRead{entered: make(chan struct{})}
	m := &sequenceModel{}
	a, err := NewRun(ctx, WithConfig(&Config{Model: m, CheckpointStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	finished := make(chan error, 1)
	go func() {
		_, runErr := a.Execute(runCtx, nil, WithCheckpointID("saved"))
		finished <- runErr
	}()
	select {
	case <-store.entered:
	case <-time.After(time.Second):
		t.Fatal("checkpoint read did not start")
	}
	_, err = a.Execute(ctx, nil)
	if err == nil {
		t.Fatal("second run entered during checkpoint read")
	}
	closed := make(chan error, 1)
	go func() { closed <- a.Close(ctx) }()
	select {
	case err = <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close blocked behind checkpoint read")
	}
	err = <-finished
	if !errors.Is(err, context.Canceled) || m.calls != 0 {
		t.Fatalf("err=%v model calls=%d", err, m.calls)
	}
	a.mu.Lock()
	active := a.active
	a.mu.Unlock()
	if active {
		t.Fatal("failed startup retained the execution claim")
	}
}
