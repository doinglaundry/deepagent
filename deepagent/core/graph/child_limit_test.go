package graph

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

type boundedChildRunner struct {
	mu           sync.Mutex
	active, peak int
	started      chan string
	release      chan struct{}
}

func (r *boundedChildRunner) Run(ctx context.Context, req tools.ChildRequest, _ types.ModelChunkSink) (*schema.Message, error) {
	r.mu.Lock()
	r.active++
	if r.active > r.peak {
		r.peak = r.active
	}
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.active--; r.mu.Unlock() }()
	r.started <- req.Prompt
	select {
	case <-r.release:
		return schema.AssistantMessage(req.Prompt, nil), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestChildAgent_ConcurrencyLimitQueuesEveryTask(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runner := &boundedChildRunner{started: make(chan string, 5), release: make(chan struct{}, 5)}
	registry, err := tools.NewRegistry(ctx, []tools.Descriptor{{Tool: tools.NewTaskTool(runner), ParallelSafe: true}})
	if err != nil {
		t.Fatal(err)
	}
	executor := newToolExecutor("run", registry, 2, nil)
	var calls []types.ToolCall
	for _, i := range []int{4, 2, 0, 3, 1} {
		calls = append(calls, types.ToolCall{ID: fmt.Sprint(i), Index: i, Name: "task", Arguments: fmt.Sprintf(`{"description":"task-%d"}`, i)})
	}
	done := make(chan struct {
		results []types.ToolResult
		err     error
	}, 1)
	go func() {
		results, err := executor.executeBatch(ctx, calls, nil)
		done <- struct {
			results []types.ToolResult
			err     error
		}{results, err}
	}()
	seen := map[string]bool{}
	for _, size := range []int{2, 2, 1} {
		for range size {
			select {
			case name := <-runner.started:
				if seen[name] {
					t.Fatalf("duplicate task %s", name)
				}
				seen[name] = true
			case <-ctx.Done():
				t.Fatal("queued task was dropped or deadlocked")
			}
		}
		for range size {
			runner.release <- struct{}{}
		}
	}
	select {
	case out := <-done:
		if out.err != nil || len(out.results) != 5 {
			t.Fatalf("results=%v err=%v", out.results, out.err)
		}
		for i, result := range out.results {
			if result.CallID != fmt.Sprint(i) || result.Content != fmt.Sprintf("task-%d", i) {
				t.Fatalf("result order lost: %+v", out.results)
			}
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.peak != 2 || runner.active != 0 || len(seen) != 5 {
		t.Fatalf("peak=%d active=%d seen=%v", runner.peak, runner.active, seen)
	}
}
