package graph

import (
	"context"
	"testing"

	"eino-cli/deepagent/core/internal/conversation"
	"github.com/cloudwego/eino/schema"
)

func TestRun_PatchDanglingToolCallsOnlyInModelRequest(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		ctx := context.Background()
		history := conversation.New("thread", nil, nil, nil)
		assistant := schema.AssistantMessage("", []schema.ToolCall{
			{ID: "done", Function: schema.FunctionCall{Name: "read_file", Arguments: "{}"}},
			{ID: "interrupted", Function: schema.FunctionCall{Name: "write_file", Arguments: "{}"}},
		})
		if err := history.AddHistory(ctx, "old", schema.UserMessage("old input"), assistant, schema.ToolMessage("already done", "done")); err != nil {
			t.Fatal(err)
		}
		m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("new answer", nil)}}}
		a, err := New(ctx, WithConfig(&Config{Model: m, Conversation: history, EnablePatchToolCalls: enabled}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Run(ctx, []*schema.Message{schema.UserMessage("continue")}); err != nil {
			t.Fatal(err)
		}
		repairs := 0
		for i, message := range m.inputs[0] {
			if message.Role == schema.Tool && message.ToolCallID == "interrupted" {
				repairs++
				if message.Content == "" || i+1 >= len(m.inputs[0]) || m.inputs[0][i+1].Role != schema.User {
					t.Fatalf("invalid repair position: %+v", m.inputs[0])
				}
			}
		}
		want := 0
		if enabled {
			want = 1
		}
		if repairs != want {
			t.Fatalf("enabled=%v repairs=%d", enabled, repairs)
		}
		for _, message := range history.History(ctx) {
			if message.Role == schema.Tool && message.ToolCallID == "interrupted" {
				t.Fatal("synthetic result persisted as real execution")
			}
		}
		if len(assistant.ToolCalls) != 2 {
			t.Fatal("original assistant changed")
		}
	}
}
