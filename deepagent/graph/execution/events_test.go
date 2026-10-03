package execution

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/graph/conversation"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/schema"
)

func TestRun_CompactionEventsAtEachSamplingBoundary(t *testing.T) {
	ctx := context.Background()
	c := &compactionEventConversation{Conversation: conversation.New("thread", nil, nil, nil)}
	chatModel := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	var events []types.RuntimeEvent
	graph, err := New(ctx, WithConfig(&Config{Model: chatModel, Conversation: c, ToolDescriptors: []tools.ToolDescriptor{{Tool: &countingTool{}}}, Emit: func(_ context.Context, e types.RuntimeEvent) error { events = append(events, e); return nil }}))
	if err != nil {
		t.Fatal(err)
	}
	_, executeErr := graph.Invoke(ctx, []*schema.Message{schema.UserMessage("go")})
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	started, finished, requests := 0, 0, 0
	for i, event := range events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("sequence=%d index=%d", event.Sequence, i)
		}
		switch event.Kind {
		case "context_compact_started":
			started++
			_, ok := event.Data.(conversation.ContextCompactStartedPayload)
			if !ok {
				t.Fatal("wrong started payload")
			}
		case "context_compacted":
			finished++
		case "llm_requesting":
			requests++
			if started != requests || finished != requests {
				t.Fatal("model sampled before compaction events")
			}
		}
	}
	if requests != 2 || c.calls != 2 {
		t.Fatalf("requests=%d compact=%d", requests, c.calls)
	}
}

func TestRun_FailedCompactionNeverPublishesSuccessOrCallsModel(t *testing.T) {
	want := errors.New("compaction store failed")
	c := &compactionEventConversation{Conversation: conversation.New("thread", nil, nil, nil), err: want}
	chatModel := &sequenceModel{}
	graph, err := New(context.Background(), WithConfig(&Config{Model: chatModel, Conversation: c, Emit: func(_ context.Context, e types.RuntimeEvent) error {
		if e.Kind == "context_compacted" {
			t.Error("published false success")
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = graph.Invoke(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if !errors.Is(err, want) || chatModel.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, chatModel.calls)
	}
}

func TestRun_AutomaticThresholdCompactionRetainsRecentInput(t *testing.T) {
	ctx := context.Background()
	history := conversation.New("thread", nil, &conversation.SummaryCompaction{Model: &paritySummaryModel{}, TokenLimit: 1, KeepRecent: 4}, nil)
	for range 8 {
		err := history.AddHistory(ctx, "old", schema.UserMessage("old user"), schema.AssistantMessage("old answer", nil))
		if err != nil {
			t.Fatal(err)
		}
	}
	chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}
	compacted := false
	graph, err := New(ctx, WithConfig(&Config{Model: chatModel, Conversation: history, Emit: func(_ context.Context, e types.RuntimeEvent) error {
		if e.Kind == "context_compacted" {
			compacted = true
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	defer graph.Close(ctx)
	_, err = graph.Invoke(ctx, []*schema.Message{schema.UserMessage("newest")})
	if err != nil {
		t.Fatal(err)
	}
	input := chatModel.inputs[0]
	if !compacted || len(input) > 7 || input[0].Content != "Earlier conversation summary:\nretained summary" || input[len(input)-1].Content != "newest" {
		t.Fatalf("compaction lost recent input: %v", input)
	}
}

func TestRun_ToolPanicDoesNotHangCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
	graph, err := New(ctx, WithConfig(&Config{Model: chatModel, ToolDescriptors: []tools.ToolDescriptor{{Tool: &panicTool{}}}}))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := graph.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "tool crashed") {
			t.Fatalf("err=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("run cleanup hung after tool panic")
	}
	closeErr := graph.Close(ctx)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if chatModel.calls != 1 {
		t.Fatal("internal tool failure was swallowed and model called again")
	}
}

func TestRun_ModelAndToolStreamsPreserveOrder(t *testing.T) {
	var events []types.RuntimeEvent
	chatModel := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("calling", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "stream_tool", Arguments: "{}"}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	graph, err := New(context.Background(), WithConfig(&Config{Model: chatModel, ToolDescriptors: []tools.ToolDescriptor{{Tool: &eventStreamTool{}}}, Emit: func(_ context.Context, event types.RuntimeEvent) error { events = append(events, event); return nil }}))
	if err != nil {
		t.Fatal(err)
	}
	_, executeErr := graph.Invoke(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if executeErr != nil {
		t.Fatal(executeErr)
	}
	start, end, chunks := -1, -1, 0
	for i, event := range events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("sequence=%d index=%d", event.Sequence, i)
		}
		switch event.Kind {
		case "tool_start":
			if start >= 0 {
				t.Fatal("duplicate start")
			}
			start = i
			state, ok := event.Data.(types.ToolStartPayload)
			if !ok || state.Name != "stream_tool" || state.Args != "{}" || state.ToolStartTime.IsZero() {
				t.Fatalf("missing start metadata: %+v", event)
			}
		case "tool_call_output_chunk":
			chunk, ok := event.Data.(types.ToolCallOutputChunkPayload)
			if !ok || chunk.Name != "stream_tool" || chunk.CallID != "call" || chunk.Chunk == "" {
				t.Fatalf("missing chunk metadata: %+v", event)
			}
			if start < 0 || end >= 0 {
				t.Fatal("chunk outside tool lifecycle")
			}
			chunks++
		case "tool_end":
			end = i
			state, ok := event.Data.(types.ToolEndPayload)
			if !ok || state.Result != "onetwo" || state.ToolStartTime.IsZero() || state.Name != "stream_tool" {
				t.Fatalf("missing end metadata: %+v", event)
			}
		}
	}
	if start < 0 || end <= start || chunks != 2 {
		t.Fatalf("start=%d end=%d chunks=%d", start, end, chunks)
	}
}

func TestRun_InputConsumedEventsFollowSuccessfulHistoryWrites(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "partial write failure"}[fail], func(t *testing.T) {
			ctx := context.Background()
			history := &inputEventConversation{Conversation: conversation.New("thread", nil, nil, nil)}
			failure := errors.New("input store failed")
			if fail {
				history.failure = failure
			}
			messages := []*schema.Message{schema.UserMessage("first"), schema.UserMessage("second")}
			messages[0].Extra = map[string]any{"message_id": "one"}
			meta := map[string]string{"Sender": "user"}
			var consumed []types.Input
			chatModel := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}
			graph, err := New(ctx, WithConfig(&Config{Model: chatModel, Conversation: history, Emit: func(ctx context.Context, event types.RuntimeEvent) error {
				if event.Kind != "input_consumed" {
					return nil
				}
				input := event.Data.(types.Input)
				persisted := history.GetHistory(ctx)
				if len(persisted) == 0 || persisted[len(persisted)-1] != input.Message {
					t.Error("event preceded successful history write")
				}
				consumed = append(consumed, input)
				return nil
			}}))
			if err != nil {
				t.Fatal(err)
			}
			defer graph.Close(ctx)
			_, err = graph.Invoke(ctx, messages, WithInputMetadata(meta, "second-meta"))
			want := 2
			if fail {
				want = 1
				if !errors.Is(err, failure) {
					t.Fatalf("err=%v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if len(consumed) != want {
				t.Fatalf("consumed=%v want=%d", consumed, want)
			}
			if consumed[0].Message != messages[0] || consumed[0].Meta.(map[string]string)["Sender"] != "user" {
				t.Fatal("input identity or metadata changed")
			}
			if fail && chatModel.calls != 0 {
				t.Fatal("model called after failed input write")
			}
		})
	}
}
