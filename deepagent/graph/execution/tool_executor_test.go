package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestRun_ReadOnlyToolSetCannotExecuteUnclassifiedTool(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}, {schema.AssistantMessage("unavailable", nil)}}}
	a, err := New(ctx, WithConfig(&Config{Model: m, ReadOnlyToolsOnly: true, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool}}}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	_, err = a.Invoke(ctx, []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if tool.count.Load() != 0 {
		t.Fatal("readonly run executed unclassified tool")
	}
	last := m.inputs[1][len(m.inputs[1])-1]
	if last.Role != schema.Tool || !strings.Contains(last.Content, "unknown tool") {
		t.Fatalf("model did not see unavailable tool: %+v", last)
	}
}

func TestPolicy_DenyPreventsExecutionAndApproval(t *testing.T) {
	for _, requiresApproval := range []bool{false, true} {
		counter := &countingTool{}
		m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
		a, err := New(context.Background(), WithConfig(&Config{
			Model:           m,
			ToolDescriptors: []tools.ToolDescriptor{{Tool: counter, RequiresApproval: requiresApproval, ReturnDirect: true}},
			Policy: tools.PolicyFunc(func(context.Context, types.ToolCall, tools.ToolDescriptor) (tools.Decision, error) {
				return tools.Decision{Action: tools.Deny, Reason: "blocked"}, nil
			}),
		}))
		if err != nil {
			t.Fatal(err)
		}
		result, err := a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("go")})
		if err != nil {
			t.Fatal(err)
		}
		if counter.count.Load() != 0 || result.Content != "blocked" {
			t.Fatalf("executed=%d result=%v", counter.count.Load(), result)
		}
		err = a.Close(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestRun_ToolPanicReleasesLedger(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	toolSet, err := tools.NewToolSet(ctx, []tools.ToolDescriptor{{Tool: &panicTool{}}})
	if err != nil {
		t.Fatal(err)
	}
	executor := newToolExecutor(toolSet, 1, nil)
	call := types.ToolCall{ID: "call", Name: "counter", Arguments: "{}"}
	_, err = executor.execute(ctx, call, nil)
	if err == nil || !strings.Contains(err.Error(), "tool crashed") {
		t.Fatalf("panic not reported: %v", err)
	}
	_, executeErr := executor.execute(ctx, call, nil)
	if executeErr == nil || !strings.Contains(executeErr.Error(), "unknown outcome") {
		t.Fatalf("panic was retried: %v", executeErr)
	}
	cancelErr := executor.cancel(ctx)
	if cancelErr != nil {
		t.Fatalf("panic left live execution: %v", cancelErr)
	}
}

func TestToolExecutor_CompletedCallIdentityCannotChange(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	toolSet, err := tools.NewToolSet(ctx, []tools.ToolDescriptor{{Tool: tool}})
	if err != nil {
		t.Fatal(err)
	}
	executor := newToolExecutor(toolSet, 1, nil)
	call := types.ToolCall{ID: "call", Name: "counter", Arguments: "original"}
	_, executorexecuteErr := executor.execute(ctx, call, nil)
	if executorexecuteErr != nil {
		t.Fatal(executorexecuteErr)
	}
	call.Arguments = "changed"
	_, executeErr := executor.execute(ctx, call, nil)
	if executeErr == nil {
		t.Fatal("reused result for changed tool arguments")
	}
	if tool.count.Load() != 1 {
		t.Fatalf("tool executed %d times", tool.count.Load())
	}
}

func TestRun_EagerToolDoesNotExecuteTwice(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{started: make(chan struct{}), release: make(chan struct{})}
	toolSet, err := tools.NewToolSet(ctx, []tools.ToolDescriptor{{Tool: tool}})
	if err != nil {
		t.Fatal(err)
	}
	e := newToolExecutor(toolSet, 2, nil)
	call := types.ToolCall{ID: "call", Name: "counter", Arguments: "one"}
	done := make(chan error, 2)
	go func() { _, err := e.execute(ctx, call, nil); done <- err }()
	<-tool.started
	go func() { _, err := e.execute(ctx, call, nil); done <- err }()
	close(tool.release)
	for range 2 {
		err := <-done
		if err != nil {
			t.Fatal(err)
		}
	}
	_, executeErr := e.execute(ctx, call, nil)
	if executeErr != nil {
		t.Fatal(executeErr)
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
	e := newToolExecutor(r, 2, nil)
	err = e.executeBatch(ctx, []types.ToolCall{{ID: "b", Index: 1, Name: "counter", Arguments: "second"}, {ID: "a", Index: 0, Name: "counter", Arguments: "first"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	states := []types.ToolCallState{{Call: types.ToolCall{ID: "a"}}, {Call: types.ToolCall{ID: "b"}}}
	e.snapshot(states)
	results := []*types.ToolResult{states[0].Result, states[1].Result}
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
	e := newToolExecutor(r, 1, nil)
	call := types.ToolCall{ID: "call", Name: "counter"}
	e.restore([]types.ToolCallState{{Call: call, Status: types.CallOutcomeUnknown}})
	_, executeErr := e.execute(ctx, call, nil)
	if executeErr == nil {
		t.Fatal("unknown outcome must block")
	}
	if tool.count.Load() != 0 {
		t.Fatal("reexecuted side effect")
	}
}

func TestToolExecutorExposesAssignedCallIdentity(t *testing.T) {
	ctx := context.Background()
	toolSet, err := tools.NewToolSet(ctx, []tools.ToolDescriptor{{Tool: &identityTool{}, ParallelSafe: true}})
	if err != nil {
		t.Fatal(err)
	}
	executor := newToolExecutor(toolSet, 2, nil)
	err = executor.executeBatch(ctx, []types.ToolCall{
		{ID: "first", Index: 0, Name: "counter", Arguments: "{}"},
		{ID: "second", Index: 1, Name: "counter", Arguments: "{}"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	states := []types.ToolCallState{{Call: types.ToolCall{ID: "first"}}, {Call: types.ToolCall{ID: "second"}}}
	executor.snapshot(states)
	results := []*types.ToolResult{states[0].Result, states[1].Result}
	if len(results) != 2 || results[0].Content != "first" || results[1].Content != "second" {
		t.Fatalf("incorrect tool context identities: %+v", results)
	}
	id := GetToolCallID(ctx)
	if id != "" {
		t.Fatalf("tool identity leaked into caller: %q", id)
	}
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
			out, err := a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("go")})
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
	answer, err := a.Invoke(ctx, []*schema.Message{schema.UserMessage("run")})
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
			e := newToolExecutor(toolSet, 1, nil)
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

func TestToolExecutor_AllInterfacesAuthorizeOnceBeforeInvocation(t *testing.T) {
	for _, deny := range []bool{false, true} {
		plain := &countingTool{}
		stream := &countingStreamTool{}
		enhanced := &imageTool{}
		enhancedStream := &imageStreamTool{}
		cases := []struct {
			name  string
			tool  tool.BaseTool
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
				e := newToolExecutor(toolSet, 1, policy)
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

func TestRun_EnhancedToolPreservesMultimodalHistoryAndState(t *testing.T) {
	tool := &imageTool{}
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "image-call", Function: schema.FunctionCall{Name: "image", Arguments: "{}"}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	var completed types.ToolCallState
	a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool}}, Emit: func(_ context.Context, event types.RuntimeEvent) error {
		if event.Kind == "tool_end" {
			payload := event.Data.(types.ToolEndPayload)
			completed = types.ToolCallState{Call: types.ToolCall{ID: payload.CallID, Name: payload.Name, Arguments: payload.ArgumentsInJSON}, StartedAt: payload.ToolStartTime, Result: &types.ToolResult{CallID: payload.CallID, Content: payload.Result, MultiContent: payload.MultiContent}}
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("show image")})
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 1 || m.calls != 2 {
		t.Fatalf("tool=%d model=%d", tool.calls, m.calls)
	}
	var found *schema.Message
	for _, msg := range m.inputs[1] {
		if msg.ToolCallID == "image-call" {
			found = msg
		}
	}
	if found == nil || len(found.UserInputMultiContent) != 2 || found.UserInputMultiContent[1].Image == nil || *found.UserInputMultiContent[1].Image.URL != "https://example.test/image.png" {
		t.Fatalf("model lost image: %+v", found)
	}
	raw, err := json.Marshal(&types.RunState{Calls: []types.ToolCallState{completed}})
	if err != nil {
		t.Fatal(err)
	}
	var restored types.RunState
	err = json.Unmarshal(raw, &restored)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored.Calls) != 1 || restored.Calls[0].Result == nil || len(restored.Calls[0].Result.MultiContent) != 2 || *restored.Calls[0].Result.MultiContent[1].Image.URL != "https://example.test/image.png" {
		t.Fatalf("missing tool result: %s", raw)
	}
}

func TestRun_EnhancedToolPolicyAndReturnDirect(t *testing.T) {
	for _, deny := range []bool{false, true} {
		tool := &imageTool{}
		m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "image-call", Function: schema.FunctionCall{Name: "image", Arguments: "{}"}}})}}}
		policies := 0
		a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, ReturnDirect: true}}, Policy: tools.PolicyFunc(func(_ context.Context, call types.ToolCall, _ tools.ToolDescriptor) (tools.Decision, error) {
			policies++
			if call.Arguments != `{}` {
				t.Errorf("policy saw original arguments: %s", call.Arguments)
			}
			if deny {
				return tools.Decision{Action: tools.Deny, Reason: "denied"}, nil
			}
			return tools.Decision{Action: tools.Allow}, nil
		})}))
		if err != nil {
			t.Fatal(err)
		}
		message, err := a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("show image")})
		if err != nil {
			t.Fatal(err)
		}
		want, calls, parts := "image description", 1, 2
		if deny {
			want, calls, parts = "denied", 0, 0
		}
		if tool.calls != calls || m.calls != 1 || policies != 1 || message.Content != want || len(message.UserInputMultiContent) != parts {
			t.Fatalf("tool=%d model=%d policy=%d message=%+v", tool.calls, m.calls, policies, message)
		}
		history := a.conversation.History(context.Background())
		if history[len(history)-1].Role != schema.Tool {
			t.Fatal("ReturnDirect mutated tool history")
		}
	}
}

func TestRun_EnhancedStreamPreservesTextOrderAndImage(t *testing.T) {
	tool := &imageStreamTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "image-call", Function: schema.FunctionCall{Name: "image_stream", Arguments: "{}"}}})}}}
	var chunks []string
	a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, ReturnDirect: true}}, Emit: func(_ context.Context, event types.RuntimeEvent) error {
		chunk, ok := event.Data.(types.ToolCallOutputChunkPayload)
		if ok {
			chunks = append(chunks, chunk.Chunk)
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	output, err := a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("show")})
	if err != nil {
		t.Fatal(err)
	}
	if tool.calls != 1 || m.calls != 1 || output.Content != "first second" || len(output.UserInputMultiContent) != 2 || *output.UserInputMultiContent[1].Image.URL != "https://example.test/stream.png" {
		t.Fatalf("tool=%d model=%d output=%+v", tool.calls, m.calls, output)
	}
	if len(chunks) != 2 || chunks[0] != "first " || chunks[1] != "second" {
		t.Fatalf("chunks=%v", chunks)
	}
}

func TestRun_EnhancedStreamPolicyAndReturnDirect(t *testing.T) {
	for _, scenario := range []string{"allow", "deny"} {
		t.Run(scenario, func(t *testing.T) {
			tool := &imageStreamTool{}
			m := enhancedStreamModel()
			var chunks string
			a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, ReturnDirect: true}}, Policy: tools.PolicyFunc(func(_ context.Context, call types.ToolCall, _ tools.ToolDescriptor) (tools.Decision, error) {
				if call.Arguments != `{}` {
					t.Errorf("wrong policy arguments: %s", call.Arguments)
				}
				if scenario == "deny" {
					return tools.Decision{Action: tools.Deny, Reason: "denied"}, nil
				}
				return tools.Decision{Action: tools.Allow}, nil
			}), Emit: func(_ context.Context, event types.RuntimeEvent) error {
				chunk, ok := event.Data.(types.ToolCallOutputChunkPayload)
				if ok {
					chunks += chunk.Chunk
				}
				return nil
			}}))
			if err != nil {
				t.Fatal(err)
			}
			output, err := a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("show")})
			if err != nil {
				t.Fatal(err)
			}
			want, calls := "first second", 1
			wantChunks := want
			if scenario == "deny" {
				want, calls = "denied", 0
				wantChunks = ""
			}
			if chunks != wantChunks || output.Content != want || tool.calls != calls || m.calls != 1 {
				t.Fatalf("chunks=%s output=%+v tool=%d model=%d", chunks, output, tool.calls, m.calls)
			}
			if scenario == "allow" && (tool.arguments != `{}` || len(output.UserInputMultiContent) != 2) {
				t.Fatalf("arguments=%s output=%+v", tool.arguments, output)
			}
		})
	}
}

func TestRun_EnhancedStreamCancelReleasesProducer(t *testing.T) {
	opened, exited := make(chan struct{}), make(chan struct{})
	tool := &imageStreamTool{run: func(ctx context.Context) (*schema.StreamReader[*schema.ToolResult], error) {
		reader, writer := schema.Pipe[*schema.ToolResult](0)
		go func() { defer close(exited); defer writer.Close(); close(opened); <-ctx.Done() }()
		return reader, nil
	}}
	a, err := New(context.Background(), WithConfig(&Config{Model: enhancedStreamModel(), ToolDescriptors: []tools.ToolDescriptor{{Tool: tool}}}))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("show")})
		done <- err
	}()
	<-opened
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	closeErr := a.Close(ctx)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	checkErr := <-done
	if !errors.Is(checkErr, context.Canceled) {
		t.Fatalf("err=%v", checkErr)
	}
	select {
	case <-exited:
	case <-ctx.Done():
		t.Fatal("producer leaked")
	}
}

func TestRun_EnhancedStreamOpenErrorClosesReturnedReader(t *testing.T) {
	reader, writer := schema.Pipe[*schema.ToolResult](0)
	defer writer.Close()
	want := errors.New("stream open failure")
	tool := &imageStreamTool{run: func(context.Context) (*schema.StreamReader[*schema.ToolResult], error) { return reader, want }}
	a, err := New(context.Background(), WithConfig(&Config{Model: enhancedStreamModel(), ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, ReturnDirect: true}}}))
	if err != nil {
		t.Fatal(err)
	}
	message, err := a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("show")})
	if err != nil || message.Content != want.Error() {
		t.Fatalf("message=%v err=%v", message, err)
	}
	closed := make(chan bool, 1)
	go func() { closed <- writer.Send(&schema.ToolResult{}, nil) }()
	select {
	case ok := <-closed:
		if !ok {
			t.Fatal("reader left open")
		}
	case <-time.After(time.Second):
		reader.Close()
		t.Fatal("reader not closed")
	}
}

func TestWebMasksApplyLocallyAndGlobally(t *testing.T) {
	ctx := context.WithValue(context.Background(), graphWebMaskContextKey{}, "present")
	cases := []struct {
		name               string
		localRejectsSearch bool
		wantGlobalCalls    int
	}{
		{"local mask rejects search", true, 3},
		{"global mask rejects search", false, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			localCalls, globalCalls := 0, 0
			config := Config{
				Model: &sequenceModel{},
				WebConfig: &tools.WebConfig{
					EnableFetchURL: true, EnableWebSearch: true, SearchURL: "https://search.example/query",
					ToolMask: func(maskCtx context.Context, info *schema.ToolInfo) bool {
						localCalls++
						if maskCtx.Value(graphWebMaskContextKey{}) != "present" {
							t.Error("web mask lost run context")
						}
						return !tc.localRejectsSearch || info.Name == "read_url"
					},
				},
				ToolDescriptors: []tools.ToolDescriptor{{Tool: &webMaskTestTool{name: "keep"}}, {Tool: &webMaskTestTool{name: "discard"}}},
				ToolMask: func(maskCtx context.Context, info *schema.ToolInfo) bool {
					globalCalls++
					if maskCtx.Value(graphWebMaskContextKey{}) != "present" {
						t.Error("global mask lost run context")
					}
					return info.Name != "discard" && info.Name != "web_search"
				},
			}
			agent, err := New(ctx, WithConfig(&config))
			if err != nil {
				t.Fatal(err)
			}
			defer agent.Close(ctx)
			infos, err := agent.tools.ModelTools(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, info := range infos {
				names = append(names, info.Name)
			}
			if !reflect.DeepEqual(names, []string{"keep", "read_url"}) || localCalls != 2 || globalCalls != tc.wantGlobalCalls {
				t.Fatalf("tools=%v local calls=%d global calls=%d", names, localCalls, globalCalls)
			}
			allowed := config.WebConfig.ToolMask(ctx, &schema.ToolInfo{Name: "discard"})
			if allowed != !tc.localRejectsSearch || globalCalls != tc.wantGlobalCalls {
				t.Fatal("caller web mask was combined with global policy")
			}
		})
	}
}
