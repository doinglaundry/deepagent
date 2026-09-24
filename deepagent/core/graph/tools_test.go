package graph

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type countingTool struct {
	count   atomic.Int32
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

type panicTool struct{ countingTool }

func (*panicTool) InvokableRun(context.Context, string, ...einotool.Option) (string, error) {
	panic("tool crashed")
}

func TestRun_ToolPanicReleasesLedger(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	registry, err := tools.NewRegistry(ctx, []tools.Descriptor{{Tool: &panicTool{}}})
	if err != nil {
		t.Fatal(err)
	}
	executor := newToolExecutor("run", registry, 1, nil)
	call := types.ToolCall{ID: "call", Name: "counter", Arguments: "{}"}
	_, err = executor.execute(ctx, call, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "tool crashed") {
		t.Fatalf("panic not reported: %v", err)
	}
	if _, err := executor.execute(ctx, call, nil, nil); err == nil || !strings.Contains(err.Error(), "unknown outcome") {
		t.Fatalf("panic was retried: %v", err)
	}
	if err := executor.cancel(ctx); err != nil {
		t.Fatalf("panic left live execution: %v", err)
	}
}

func TestToolExecutor_CompletedCallIdentityCannotChange(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	registry, err := tools.NewRegistry(ctx, []tools.Descriptor{{Tool: tool}})
	if err != nil {
		t.Fatal(err)
	}
	executor := newToolExecutor("run", registry, 1, nil)
	call := types.ToolCall{ID: "call", Name: "counter", Arguments: "original"}
	if _, err := executor.execute(ctx, call, nil, nil); err != nil {
		t.Fatal(err)
	}
	call.Arguments = "changed"
	if _, err := executor.execute(ctx, call, nil, nil); err == nil {
		t.Fatal("reused result for changed tool arguments")
	}
	if tool.count.Load() != 1 {
		t.Fatalf("tool executed %d times", tool.count.Load())
	}
}

func (*countingTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "counter"}, nil
}
func (t *countingTool) InvokableRun(ctx context.Context, args string, _ ...einotool.Option) (string, error) {
	t.count.Add(1)
	if t.started != nil {
		t.once.Do(func() { close(t.started) })
	}
	if t.release != nil {
		select {
		case <-t.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return args, nil
}
func TestRun_EagerToolDoesNotExecuteTwice(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{started: make(chan struct{}), release: make(chan struct{})}
	registry, err := tools.NewRegistry(ctx, []tools.Descriptor{{Tool: tool}})
	if err != nil {
		t.Fatal(err)
	}
	e := newToolExecutor("run", registry, 2, nil)
	call := types.ToolCall{ID: "call", Name: "counter", Arguments: "one"}
	done := make(chan error, 2)
	go func() { _, err := e.execute(ctx, call, nil, nil); done <- err }()
	<-tool.started
	go func() { _, err := e.execute(ctx, call, nil, nil); done <- err }()
	close(tool.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.execute(ctx, call, nil, nil); err != nil {
		t.Fatal(err)
	}
	if tool.count.Load() != 1 {
		t.Fatalf("executed %d times", tool.count.Load())
	}
}
func TestTools_ParallelResultsPersistInCallOrder(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	r, err := tools.NewRegistry(ctx, []tools.Descriptor{{Tool: tool, ParallelSafe: true}})
	if err != nil {
		t.Fatal(err)
	}
	e := newToolExecutor("run", r, 2, nil)
	results, err := e.executeBatch(ctx, []types.ToolCall{{ID: "b", Index: 1, Name: "counter", Arguments: "second"}, {ID: "a", Index: 0, Name: "counter", Arguments: "first"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Content != "first" || results[1].Content != "second" {
		t.Fatalf("out of order: %v", results)
	}
}
func TestCheckpoint_OutcomeUnknownToolIsNotReexecuted(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	r, err := tools.NewRegistry(ctx, []tools.Descriptor{{Tool: tool}})
	if err != nil {
		t.Fatal(err)
	}
	e := newToolExecutor("run", r, 1, nil)
	call := types.ToolCall{ID: "call", Name: "counter"}
	e.restore([]types.ToolCallState{{Call: call, Status: types.CallOutcomeUnknown}})
	if _, err := e.execute(ctx, call, nil, nil); err == nil {
		t.Fatal("unknown outcome must block")
	}
	if tool.count.Load() != 0 {
		t.Fatal("reexecuted side effect")
	}
}
