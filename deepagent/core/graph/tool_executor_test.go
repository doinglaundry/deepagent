package graph

import (
	"context"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
	toolSet, err := tools.NewToolSet(ctx, []tools.ToolDescriptor{{Tool: &panicTool{}}})
	if err != nil {
		t.Fatal(err)
	}
	executor := newToolExecutor("run", toolSet, 1, nil)
	call := types.ToolCall{ID: "call", Name: "counter", Arguments: "{}"}
	_, err = executor.execute(ctx, call, nil)
	if err == nil || !strings.Contains(err.Error(), "tool crashed") {
		t.Fatalf("panic not reported: %v", err)
	}
	if _, err := executor.execute(ctx, call, nil); err == nil || !strings.Contains(err.Error(), "unknown outcome") {
		t.Fatalf("panic was retried: %v", err)
	}
	if err := executor.cancel(ctx); err != nil {
		t.Fatalf("panic left live execution: %v", err)
	}
}

func TestToolExecutor_CompletedCallIdentityCannotChange(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	toolSet, err := tools.NewToolSet(ctx, []tools.ToolDescriptor{{Tool: tool}})
	if err != nil {
		t.Fatal(err)
	}
	executor := newToolExecutor("run", toolSet, 1, nil)
	call := types.ToolCall{ID: "call", Name: "counter", Arguments: "original"}
	if _, err := executor.execute(ctx, call, nil); err != nil {
		t.Fatal(err)
	}
	call.Arguments = "changed"
	if _, err := executor.execute(ctx, call, nil); err == nil {
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
	toolSet, err := tools.NewToolSet(ctx, []tools.ToolDescriptor{{Tool: tool}})
	if err != nil {
		t.Fatal(err)
	}
	e := newToolExecutor("run", toolSet, 2, nil)
	call := types.ToolCall{ID: "call", Name: "counter", Arguments: "one"}
	done := make(chan error, 2)
	go func() { _, err := e.execute(ctx, call, nil); done <- err }()
	<-tool.started
	go func() { _, err := e.execute(ctx, call, nil); done <- err }()
	close(tool.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.execute(ctx, call, nil); err != nil {
		t.Fatal(err)
	}
	if tool.count.Load() != 1 {
		t.Fatalf("executed %d times", tool.count.Load())
	}
}
func TestTools_ParallelResultsPersistInCallOrder(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	r, err := tools.NewToolSet(ctx, []tools.ToolDescriptor{{Tool: tool, ParallelSafe: true}})
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
	r, err := tools.NewToolSet(ctx, []tools.ToolDescriptor{{Tool: tool}})
	if err != nil {
		t.Fatal(err)
	}
	e := newToolExecutor("run", r, 1, nil)
	call := types.ToolCall{ID: "call", Name: "counter"}
	e.restore([]types.ToolCallState{{Call: call, Status: types.CallOutcomeUnknown}})
	if _, err := e.execute(ctx, call, nil); err == nil {
		t.Fatal("unknown outcome must block")
	}
	if tool.count.Load() != 0 {
		t.Fatal("reexecuted side effect")
	}
}

type identityTool struct{ countingTool }

func (*identityTool) InvokableRun(ctx context.Context, _ string, _ ...einotool.Option) (string, error) {
	return GetToolCallID(ctx), nil
}

func TestToolExecutorExposesAssignedCallIdentity(t *testing.T) {
	ctx := context.Background()
	toolSet, err := tools.NewToolSet(ctx, []tools.ToolDescriptor{{Tool: &identityTool{}, ParallelSafe: true}})
	if err != nil {
		t.Fatal(err)
	}
	executor := newToolExecutor("run", toolSet, 2, nil)
	results, err := executor.executeBatch(ctx, []types.ToolCall{
		{ID: "first", Index: 0, Name: "counter", Arguments: "{}"},
		{ID: "second", Index: 1, Name: "counter", Arguments: "{}"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Content != "first" || results[1].Content != "second" {
		t.Fatalf("incorrect tool context identities: %+v", results)
	}
	if id := GetToolCallID(ctx); id != "" {
		t.Fatalf("tool identity leaked into caller: %q", id)
	}
}

type failingContractTool struct {
	countingTool
	failure error
}

func (t *failingContractTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "", t.failure
}

func TestRun_ToolErrorVisibleButCancellationStopsGraph(t *testing.T) {
	for _, failure := range []error{errors.New("ordinary tool failure"), context.Canceled, context.DeadlineExceeded} {
		t.Run(failure.Error(), func(t *testing.T) {
			m := &sequenceModel{responses: [][]*schema.Message{
				{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
				{schema.AssistantMessage("handled", nil)},
			}}
			a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: &failingContractTool{failure: failure}}}}))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(context.Background())
			out, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")})
			if errors.Is(failure, context.Canceled) || errors.Is(failure, context.DeadlineExceeded) {
				if !errors.Is(err, failure) || m.calls != 1 {
					t.Fatalf("cancellation swallowed: err=%v calls=%d", err, m.calls)
				}
				return
			}
			if err != nil || out == nil || out.Content != "handled" || m.calls != 2 {
				t.Fatalf("ordinary error aborted Graph: out=%v err=%v calls=%d", out, err, m.calls)
			}
			found := false
			for _, message := range m.inputs[1] {
				if message.Role == schema.Tool && message.ToolCallID == "call" && strings.Contains(message.Content, failure.Error()) {
					found = true
				}
			}
			if !found {
				t.Fatal("tool error not visible in next model request")
			}
		})
	}
}

func TestRun_PolicyAndExecutionReceiveModelArguments(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{
		{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: `{"value":"hello"}`}},
	})}}}
	checked := 0
	a, err := New(ctx, WithConfig(&Config{
		Model:           m,
		ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, ReturnDirect: true}},
		Policy: tools.PolicyFunc(func(_ context.Context, call types.ToolCall, _ tools.ToolDescriptor) (tools.Decision, error) {
			checked++
			if call.Arguments != `{"value":"hello"}` {
				t.Fatalf("policy saw unexpected arguments: %q", call.Arguments)
			}
			return tools.Decision{Action: tools.Allow}, nil
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	answer, err := a.Run(ctx, []*schema.Message{schema.UserMessage("run")})
	if err != nil {
		t.Fatal(err)
	}
	if answer.Content != `{"value":"hello"}` || tool.count.Load() != 1 || m.calls != 1 || checked != 1 {
		t.Fatalf("answer=%+v executions=%d models=%d checked=%d", answer, tool.count.Load(), m.calls, checked)
	}
}

func TestToolExecutor_RestoreUsesOneCallState(t *testing.T) {
	for _, status := range []types.CallStatus{
		types.CallPending, types.CallBlocked, types.CallCompleted,
		types.CallRunning, types.CallOutcomeUnknown,
	} {
		t.Run(string(status), func(t *testing.T) {
			ctx := context.Background()
			counter := &countingTool{}
			toolSet, err := tools.NewToolSet(ctx, []tools.ToolDescriptor{{Tool: counter}})
			if err != nil {
				t.Fatal(err)
			}
			e := newToolExecutor("run", toolSet, 1, nil)
			defer e.cancel(ctx)
			call := types.ToolCall{ID: "call", Name: "counter", Arguments: "{}"}
			state := types.ToolCallState{Call: call, Status: status, StartedAt: time.Unix(10, 0)}
			if status == types.CallCompleted {
				state.Result = &types.ToolResult{CallID: call.ID, Content: "saved"}
			}
			e.restore([]types.ToolCallState{state})
			snapshot := []types.ToolCallState{{Call: call}}
			e.snapshot(snapshot)
			expectedStatus := status
			if status == types.CallRunning {
				expectedStatus = types.CallOutcomeUnknown
			}
			if snapshot[0].Status != expectedStatus || snapshot[0].StartedAt != state.StartedAt {
				t.Fatalf("restored snapshot=%+v", snapshot[0])
			}
			result, err := e.execute(ctx, call, nil)
			switch status {
			case types.CallRunning, types.CallOutcomeUnknown:
				if err == nil || counter.count.Load() != 0 {
					t.Fatalf("replayed unknown outcome: err=%v count=%d", err, counter.count.Load())
				}
			case types.CallCompleted:
				if err != nil || result.Content != "saved" || counter.count.Load() != 0 {
					t.Fatalf("completed call replayed: result=%v err=%v", result, err)
				}
				result.Content = "caller mutation"
				e.snapshot(snapshot)
				if snapshot[0].Result.Content != "saved" {
					t.Fatal("caller mutated saved result")
				}
			default:
				if err != nil || counter.count.Load() != 1 {
					t.Fatalf("pending/blocked call not resumed: err=%v count=%d", err, counter.count.Load())
				}
			}
		})
	}
}

type countingStreamTool struct{ countingTool }

func (t *countingStreamTool) StreamableRun(context.Context, string, ...einotool.Option) (*schema.StreamReader[string], error) {
	t.count.Add(1)
	return schema.StreamReaderFromArray([]string{"done"}), nil
}

func TestToolExecutor_AllInterfacesAuthorizeOnceBeforeInvocation(t *testing.T) {
	for _, deny := range []bool{false, true} {
		plain := &countingTool{}
		stream := &countingStreamTool{}
		enhanced := &imageTool{}
		enhancedStream := &imageStreamTool{}
		cases := []struct {
			name  string
			tool  einotool.BaseTool
			calls func() int
		}{
			{"plain", plain, func() int { return int(plain.count.Load()) }},
			{"stream", stream, func() int { return int(stream.count.Load()) }},
			{"enhanced", enhanced, func() int { return enhanced.calls }},
			{"enhanced_stream", enhancedStream, func() int { return enhancedStream.calls }},
		}
		for _, item := range cases {
			t.Run(item.name+"/"+fmt.Sprint(deny), func(t *testing.T) {
				ctx := context.Background()
				toolSet, err := tools.NewToolSet(ctx, []tools.ToolDescriptor{{Tool: item.tool}})
				if err != nil {
					t.Fatal(err)
				}
				info, err := item.tool.Info(ctx)
				if err != nil {
					t.Fatal(err)
				}
				decisions := 0
				policy := tools.PolicyFunc(func(context.Context, types.ToolCall, tools.ToolDescriptor) (tools.Decision, error) {
					decisions++
					if item.calls() != 0 {
						t.Fatal("tool executed before authorization")
					}
					action := tools.Allow
					if deny {
						action = tools.Deny
					}
					return tools.Decision{Action: action}, nil
				})
				e := newToolExecutor("run", toolSet, 1, policy)
				defer e.cancel(ctx)
				call := types.ToolCall{ID: "call", Name: info.Name, Arguments: "{}"}
				// Repeated calls must reuse the outer execution record.
				for range 2 {
					result, err := e.execute(ctx, call, nil)
					if err != nil || result == nil || result.IsError != deny {
						t.Fatalf("result=%v err=%v", result, err)
					}
				}
				expected := 1
				if deny {
					expected = 0
				}
				if decisions != 1 || item.calls() != expected {
					t.Fatalf("policy=%d tool=%d", decisions, item.calls())
				}
			})
		}
	}
}
