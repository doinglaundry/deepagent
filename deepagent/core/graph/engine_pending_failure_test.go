package graph

import (
	"context"
	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"eino-cli/deepagent/protocol"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/schema"
	"os"
	"strings"
	"testing"
)

type pendingFailureConversation struct {
	*conversation.Conversation
	failure error
}

func (c *pendingFailureConversation) AddHistory(ctx context.Context, runID string, messages ...*schema.Message) error {
	for _, message := range messages {
		if message.Extra["message_id"] == "pending" && c.failure != nil {
			return c.failure
		}
	}
	return c.Conversation.AddHistory(ctx, runID, messages...)
}
func TestCheckpoint_LegacyPendingInputsPersistOnTerminalFailure(t *testing.T) {
	for _, scenario := range []string{"budget", "cancel", "store failure"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			raw, err := os.ReadFile("../runtime/checkpointer/testdata/legacy_engine_approval.json")
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if err = json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			wire["pending"], _ = json.Marshal([]protocol.Input{{ID: "pending", Kind: protocol.InputUser, Text: "keep this accepted input"}})
			raw, _ = json.Marshal(wire)
			var messages []*schema.Message
			if err = json.Unmarshal(wire["messages"], &messages); err != nil {
				t.Fatal(err)
			}
			history := &pendingFailureConversation{Conversation: conversation.New("thread", nil, nil, nil)}
			if err = history.AddHistory(ctx, "run", messages...); err != nil {
				t.Fatal(err)
			}
			failure := errors.New("pending persistence failed")
			if scenario == "store failure" {
				history.failure = failure
			}
			store := &checkpointMemory{values: map[string][]byte{"fixture": raw}}
			counter := &countingTool{}
			m := &sequenceModel{}
			cfg := Config{ThreadID: "thread", RunID: "run", Model: m, Conversation: history, MaxModelCalls: 1, CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: counter, RequiresApproval: true}}}
			if scenario == "cancel" {
				cfg.Emit = func(_ context.Context, event types.RuntimeEvent) error {
					if event.Kind == "tool_end" {
						cancel()
					}
					return nil
				}
			}
			a, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(context.Background())
			_, err = a.Run(ctx, nil, WithCheckpointID("fixture"), WithResume("gate"), WithResumeData(map[string]any{"gate": &tools.ApprovalResult{CallID: "blocked", Approved: true}}))
			if err == nil {
				t.Fatal("expected terminal failure")
			}
			if scenario == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if scenario != "cancel" && !strings.Contains(err.Error(), "maximum model calls") {
				t.Fatal(err)
			}
			if scenario == "store failure" {
				if !errors.Is(err, failure) {
					t.Fatalf("persistence failure swallowed: %v", err)
				}
				if len(history.History(context.Background())) != 4 {
					t.Fatal("failed save mutated history")
				}
			} else {
				got := history.History(context.Background())
				if len(got) != 5 || got[4].Extra["message_id"] != "pending" || got[4].Content != "keep this accepted input" {
					t.Fatalf("accepted input lost: %+v", got)
				}
			}
			if counter.count.Load() != 1 || m.calls != 0 {
				t.Fatal("failure cleanup executed tool or model")
			}
		})
	}
}
