package graph

import (
	"context"
	"errors"
	"testing"

	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

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
