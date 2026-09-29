package graph

import (
	"context"
	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"errors"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"strings"
	"testing"
	"time"
)

type eventStreamTool struct{}

func (*eventStreamTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "stream_tool"}, nil
}

func TestRun_ToolPanicDoesNotHangCleanup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
	a, err := New(ctx, WithConfig(&Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: &panicTool{}}}}))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := a.Run(ctx, []*schema.Message{schema.UserMessage("go")}); done <- err }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "tool crashed") {
			t.Fatalf("err=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("run cleanup hung after tool panic")
	}
	if err := a.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if m.calls != 1 {
		t.Fatal("internal tool failure was swallowed and model called again")
	}
}
func (*eventStreamTool) StreamableRun(context.Context, string, ...einotool.Option) (*schema.StreamReader[string], error) {
	return schema.StreamReaderFromArray([]string{"one", "two"}), nil
}

func TestRun_ModelAndToolStreamsPreserveOrder(t *testing.T) {
	var events []types.RuntimeEvent
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("calling", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "stream_tool", Arguments: "{}"}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	a, err := New(context.Background(), WithConfig(&Config{Model: m, ToolDescriptors: []tools.ToolDescriptor{{Tool: &eventStreamTool{}}}, Emit: func(_ context.Context, e types.RuntimeEvent) error { events = append(events, e); return nil }}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}); err != nil {
		t.Fatal(err)
	}
	start, end, chunks := -1, -1, 0
	for i, e := range events {
		if e.Sequence != uint64(i+1) {
			t.Fatalf("sequence=%d index=%d", e.Sequence, i)
		}
		switch e.Kind {
		case "tool_start":
			if start >= 0 {
				t.Fatal("duplicate start")
			}
			start = i
			state, ok := e.Data.(types.ToolStartPayload)
			if !ok || state.Name != "stream_tool" || state.Args != "{}" || state.ToolStartTime.IsZero() {
				t.Fatalf("missing start metadata: %+v", e)
			}
		case "tool_call_output_chunk":
			chunk, ok := e.Data.(types.ToolCallOutputChunkPayload)
			if !ok || chunk.Name != "stream_tool" || chunk.CallID != "call" || chunk.Chunk == "" {
				t.Fatalf("missing chunk metadata: %+v", e)
			}
			if start < 0 || end >= 0 {
				t.Fatal("chunk outside tool lifecycle")
			}
			chunks++
		case "tool_end":
			end = i
			state, ok := e.Data.(types.ToolEndPayload)
			if !ok || state.Result != "onetwo" || state.ToolStartTime.IsZero() || state.Name != "stream_tool" {
				t.Fatalf("missing end metadata: %+v", e)
			}
		}
	}
	if start < 0 || end <= start || chunks != 2 {
		t.Fatalf("start=%d end=%d chunks=%d", start, end, chunks)
	}
}

type inputEventConversation struct {
	*conversation.Conversation
	failure error
}

func (c *inputEventConversation) AddHistory(ctx context.Context, run string, messages ...*schema.Message) error {
	if c.failure != nil && messages[0].Content == "second" {
		return c.failure
	}
	return c.Conversation.AddHistory(ctx, run, messages...)
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
			m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}
			a, err := New(ctx, WithConfig(&Config{Model: m, Conversation: history, Emit: func(ctx context.Context, event types.RuntimeEvent) error {
				if event.Kind != "input_consumed" {
					return nil
				}
				input := event.Data.(types.Input)
				persisted := history.History(ctx)
				if len(persisted) == 0 || persisted[len(persisted)-1] != input.Message {
					t.Error("event preceded successful history write")
				}
				consumed = append(consumed, input)
				return nil
			}}))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(ctx)
			_, err = a.Run(ctx, messages, WithInputMetadata(meta, "second-meta"))
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
			if fail && m.calls != 0 {
				t.Fatal("model called after failed input write")
			}
		})
	}
}
