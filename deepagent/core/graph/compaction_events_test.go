package graph

import (
	"context"
	"errors"
	"testing"

	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

type compactionEventConversation struct {
	*conversation.Conversation
	calls int
	err   error
}

func (*compactionEventConversation) CompactNeeded(context.Context) bool { return true }
func (c *compactionEventConversation) Compact(context.Context, string) (*conversation.ContextCompactedPayload, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return &conversation.ContextCompactedPayload{StrategyID: "test"}, nil
}
func TestRun_CompactionEventsAtEachSamplingBoundary(t *testing.T) {
	ctx := context.Background()
	c := &compactionEventConversation{Conversation: conversation.New("thread", nil, nil, nil)}
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	var events []types.RuntimeEvent
	a, err := New(ctx, WithConfig(&Config{Model: m, Conversation: c, ToolDescriptors: []tools.Descriptor{{Tool: &countingTool{}}}, Emit: func(_ context.Context, e types.RuntimeEvent) error { events = append(events, e); return nil }}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(ctx, []*schema.Message{schema.UserMessage("go")}); err != nil {
		t.Fatal(err)
	}
	started, finished, requests := 0, 0, 0
	for i, event := range events {
		if event.Sequence != uint64(i+1) {
			t.Fatalf("sequence=%d index=%d", event.Sequence, i)
		}
		switch event.Kind {
		case "context_compact_started":
			started++
			if _, ok := event.Data.(conversation.ContextCompactStartedPayload); !ok {
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
	m := &sequenceModel{}
	a, err := New(context.Background(), WithConfig(&Config{Model: m, Conversation: c, Emit: func(_ context.Context, e types.RuntimeEvent) error {
		if e.Kind == "context_compacted" {
			t.Error("published false success")
		}
		return nil
	}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")})
	if !errors.Is(err, want) || m.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, m.calls)
	}
}
