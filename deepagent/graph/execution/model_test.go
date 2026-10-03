package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestModelStreamPreservesInterleavedCallsAndFinalUsage(t *testing.T) {
	first, second := 90, 3
	chunks := []*schema.Message{
		nil,
		{Role: schema.Assistant, Content: "thinking", ToolCalls: []schema.ToolCall{
			{Index: &first, Function: schema.FunctionCall{Name: "read_file", Arguments: `{"path":`}},
			{Index: &second, ID: "second", Function: schema.FunctionCall{Name: "read_file", Arguments: `{"path":"b"}`}},
		}},
		{Content: " done", ReasoningContent: "reason", ToolCalls: []schema.ToolCall{
			{Index: &first, ID: "first", Function: schema.FunctionCall{Arguments: `"a"}`}},
			{Index: &second, Function: schema.FunctionCall{Arguments: " "}},
		}},
		{ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{TotalTokens: 12}}},
	}
	m := &sequenceModel{responses: [][]*schema.Message{chunks,
		{schema.AssistantMessage("done", nil)}, {schema.AssistantMessage("new", nil)},
	}}
	seen := 0
	var complete *schema.Message
	cfg := Config{Model: m, Emit: func(_ context.Context, event types.RuntimeEvent) error {
		if complete == nil && event.Kind == "llm_token" {
			seen++
		}
		if event.Kind == "llm_end" {
			message := event.Data.(types.LLMEnd).Message
			if len(message.ToolCalls) > 0 {
				complete = message
			}
		}
		return nil
	}}
	a, err := New(context.Background(), WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	_, err = a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if err != nil {
		t.Fatal(err)
	}
	if complete == nil || seen != 3 || complete.Content != "thinking done" || complete.ReasoningContent != "reason" || complete.ResponseMeta.Usage.TotalTokens != 12 {
		t.Fatalf("lost model output: seen=%d message=%+v", seen, complete)
	}
	if len(complete.ToolCalls) != 2 || complete.ToolCalls[0].ID != "first" || complete.ToolCalls[1].ID != "second" {
		t.Fatalf("tool identity or order lost: %+v", complete.ToolCalls)
	}
	for i, call := range complete.ToolCalls {
		if call.Index == nil || *call.Index != i {
			t.Fatalf("provider index leaked: %+v", call)
		}
	}
	if complete.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Fatalf("fragment lost: %+v", complete.ToolCalls[0])
	}
	// Fresh Graphs must not retain any collector state from earlier streams.
	cfg.Emit = nil
	next, err := New(context.Background(), WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(context.Background())
	message, err := next.Invoke(context.Background(), []*schema.Message{schema.UserMessage("next")})
	if err != nil || message.Content != "new" || len(message.ToolCalls) != 0 {
		t.Fatalf("cross-stream state: message=%+v err=%v", message, err)
	}
}

type failedModelStream struct {
	sequenceModel
	reader *schema.StreamReader[*schema.Message]
}

func (m *failedModelStream) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	return m.reader, nil
}

func TestModelStreamPropagatesStreamFailureWithoutReplay(t *testing.T) {
	want := errors.New("provider stream failed")
	stream, writer := schema.Pipe[*schema.Message](2)
	writer.Send(schema.AssistantMessage("partial", nil), nil)
	writer.Send(nil, want)
	writer.Close()
	m := &failedModelStream{reader: stream}
	a, err := New(context.Background(), WithModel(m))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(context.Background())
	_, err = a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if !errors.Is(err, want) || m.calls != 1 {
		t.Fatalf("stream error swallowed or replayed: err=%v calls=%d", err, m.calls)
	}
	if len(a.conversation.History(context.Background())) != 1 {
		t.Fatal("partial model response committed to history")
	}
}

func TestRepairToolArgumentsOnlyUnambiguousSyntax(t *testing.T) {
	for _, s := range []string{"```json\n{\"path\":\"x\",}\n```", `{"a":[1,2,],"literal":",}"}`} {
		out, e := repairToolArguments(s)
		if e != nil || !json.Valid([]byte(out)) {
			t.Fatal(out, e)
		}
	}
	for _, s := range []string{`{path:"x"}`, `{"path":`, "text {\"a\":1}"} {
		_, e := repairToolArguments(s)
		if e == nil {
			t.Fatal("invented missing JSON", s)
		}
	}
}

func TestCollectorRepairsOnlyUnambiguousJSONAtStreamEnd(t *testing.T) {
	collector := &toolCallBuffer{}
	_, err := collector.add([]schema.ToolCall{{
		ID: "call", Function: schema.FunctionCall{Name: "read_file", Arguments: "```json\n{\"path\":\"a.go\",}\n```"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	calls, err := collector.finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 || calls[0].Function.Arguments != `{"path":"a.go"}` {
		t.Fatalf("repaired calls = %+v", calls)
	}

	_, err = collector.add([]schema.ToolCall{{
		ID: "bad", Function: schema.FunctionCall{Name: "read_file", Arguments: `{path:"a.go"}`},
	}})
	if err != nil {
		t.Fatal(err)
	}
	calls, err = collector.finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[1].Function.Arguments != `{path:"a.go"}` {
		t.Fatalf("ambiguous JSON was invented or silently dropped: %+v", calls)
	}
}

func TestRun_TokenEventsAccumulateWithoutChangingContextUsage(t *testing.T) {
	ctx := context.Background()
	m := &sequenceModel{responses: [][]*schema.Message{
		{usageReply("", schema.ToolCall{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}})},
		{usageReply("done")}, {usageReply("new run")},
	}}
	var totals []types.Usage
	a, err := New(ctx, WithConfig(&Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: &countingTool{}}}, Emit: func(_ context.Context, e types.RuntimeEvent) error {
		if e.Kind == "tokens" {
			totals = append(totals, e.Data.(types.Usage))
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	for i, want := range []int64{10, 5} {
		if i > 0 {
			cfg := a.cfg
			cfg.Conversation = a.conversation
			a, err = New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(ctx)
		}
		_, err = a.Invoke(ctx, []*schema.Message{schema.UserMessage("go")})
		if err != nil {
			t.Fatal(err)
		}
		if a.state.Usage.TotalTokens != want {
			t.Fatalf("run %d cumulative=%+v", i, a.state.Usage)
		}
		got := a.conversation.ContextUsage()
		if got.LastModelTotal != 5 || got.CurrentTotal != 5 {
			t.Fatalf("context usage must remain last request: %+v", got)
		}
	}
	if len(totals) != 3 || totals[0].TotalTokens != 5 || totals[1] != (types.Usage{PromptTokens: 6, CompletionTokens: 4, TotalTokens: 10}) || totals[2].TotalTokens != 5 {
		t.Fatalf("token events=%+v", totals)
	}
}

func TestCheckpoint_ResumeContinuesCumulativeUsage(t *testing.T) {
	ctx := context.Background()
	m := &sequenceModel{responses: [][]*schema.Message{
		{usageReply("", schema.ToolCall{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}})},
		{usageReply("done")},
	}}
	cfg := Config{ThreadID: "thread", RunID: "run", Model: m, CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.ToolDescriptor{{Tool: &countingTool{}, RequiresApproval: true}}}
	first, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatal(err)
	}
	err = first.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A different Conversation forces restoration from the checkpoint snapshot.
	next, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(ctx)
	err = next.conversation.AddHistory(ctx, "run", first.conversation.History(ctx)...)
	if err != nil {
		t.Fatal(err)
	}
	_, err = next.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{Approved: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if next.state.Usage != (types.Usage{PromptTokens: 6, CompletionTokens: 4, TotalTokens: 10}) {
		t.Fatalf("restored usage=%+v", next.state.Usage)
	}
}

func TestRun_LegacyExtraUsageUsesConversation(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		t.Run(fmt.Sprint(metadata), func(t *testing.T) {
			response := schema.AssistantMessage("done", nil)
			response.Extra = map[string]any{"prompt_tokens": int64(7), "completion_tokens": float64(3)}
			want := int64(10)
			if metadata {
				response.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5}}
				want = 5
			}
			var totals []types.Usage
			a, err := New(context.Background(), WithConfig(&Config{Model: &sequenceModel{responses: [][]*schema.Message{{response}}}, Emit: func(_ context.Context, event types.RuntimeEvent) error {
				if event.Kind == "tokens" {
					totals = append(totals, event.Data.(types.Usage))
				}
				return nil
			}}))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(context.Background())
			_, executeErr := a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("go")})
			if executeErr != nil {
				t.Fatal(executeErr)
			}
			if len(totals) != 1 || totals[0].TotalTokens != want || a.state.Usage != totals[0] || a.conversation.RunUsage() != totals[0] || a.conversation.ContextUsage().LastModelTotal != want {
				t.Fatalf("events=%+v snapshot=%+v context=%+v", totals, a.state.Usage, a.conversation.ContextUsage())
			}
		})
	}
}

func TestRootResumeRestoresProviderContextBeforeNextModel(t *testing.T) {
	ctx := context.Background()
	m := &sequenceModel{responses: [][]*schema.Message{
		{usageReply("", schema.ToolCall{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}})},
		{usageReply("done")},
	}}
	cfg := Config{ThreadID: "thread", RunID: "run", Model: m, CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.ToolDescriptor{{Tool: &countingTool{}, RequiresApproval: true}}}
	first, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = first.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatal(err)
	}
	saved := first.conversation.ContextUsage()
	err = first.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	var next *Graph
	cfg.Emit = func(_ context.Context, event types.RuntimeEvent) error {
		if event.Kind == "run_state_restored" {
			got := next.conversation.ContextUsage()
			if got != saved {
				return fmt.Errorf("provider baseline not restored: got=%+v saved=%+v", got, saved)
			}
			checked = true
		}
		return nil
	}
	next, err = New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close(ctx)
	err = next.conversation.AddHistory(ctx, "run", first.conversation.History(ctx)...)
	if err != nil {
		t.Fatal(err)
	}
	_, err = next.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{Approved: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("restored context boundary not observed")
	}
}
