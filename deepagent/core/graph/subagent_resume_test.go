package graph

import (
	"context"
	"fmt"
	"testing"

	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestChildAgent_ApprovalResumesNestedGraphOnNewParent(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, allow := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/allow=%v", streaming, allow), func(t *testing.T) { testChildApprovalResume(t, streaming, allow) })
		}
	}
}
func testChildApprovalResume(t *testing.T, streaming, allow bool) {
	ctx := context.Background()
	first, approved := &namedCountingTool{name: "first"}, &namedCountingTool{name: "approved"}
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "child-task", Function: schema.FunctionCall{Name: "task", Arguments: `{"description":"perform child work"}`}}})},
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "first", Function: schema.FunctionCall{Name: "first", Arguments: "{}"}}, {ID: "approved", Function: schema.FunctionCall{Name: "approved", Arguments: "{}"}}})},
		{schema.AssistantMessage("child done", nil)},
		{schema.AssistantMessage("parent done", nil)},
	}}
	store := &checkpointMemory{}
	injections := 0
	cfg := Config{SubAgentContextInjector: func(context.Context, string) ([]*schema.Message, error) {
		injections++
		if injections > 1 {
			return nil, fmt.Errorf("context injector executed again during resume")
		}
		return []*schema.Message{schema.SystemMessage("injected context")}, nil
	}, EnableSubAgentTaskStreaming: streaming, Model: m, ThreadID: "parent", RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: first}, {Tool: approved, RequiresApproval: true}}}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("delegate")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok || len(info.InterruptContexts) != 1 {
		t.Fatalf("missing child approval: %v %+v", err, info)
	}
	if first.count != 1 || approved.count != 0 || m.calls != 2 {
		t.Fatalf("before resume counts=%d/%d model=%d", first.count, approved.count, m.calls)
	}
	if len(store.values) != 1 {
		t.Fatalf("child created external sidecar: %d entries", len(store.values))
	}
	if len(a.state.Extensions["child_checkpoint/child-task"]) == 0 {
		t.Fatal("child checkpoint not embedded")
	}
	cfg.Conversation = a.conversation
	restored, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	out, err := restored.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "approved", Approved: allow}}))
	if err != nil {
		t.Fatal(err)
	}
	wantApproved := 0
	if allow {
		wantApproved = 1
	}
	if out.Content != "parent done" || m.calls != 4 || first.count != 1 || approved.count != wantApproved {
		t.Fatalf("out=%v model=%d counts=%d/%d phase=%s", out, m.calls, first.count, approved.count, restored.state.Phase)
	}
	if len(restored.state.Extensions["child_checkpoint/child-task"]) != 0 {
		t.Fatal("completed child retained stale checkpoint")
	}
	if injections != 1 {
		t.Fatalf("injections=%d", injections)
	}
	childInput := m.inputs[2]
	foundPrompt, foundAssistant, foundFirst, foundApproved := false, false, false, false
	for _, message := range childInput {
		if message.Role == schema.User && message.Content == "perform child work" {
			foundPrompt = true
		}
		if len(message.ToolCalls) == 2 {
			foundAssistant = true
		}
		if message.Role == schema.Tool && message.ToolCallID == "first" {
			foundFirst = true
		}
		if message.Role == schema.Tool && message.ToolCallID == "approved" {
			foundApproved = true
		}
	}
	if !foundPrompt || !foundAssistant || !foundFirst || !foundApproved {
		t.Fatalf("restored child lost conversation: %+v", childInput)
	}
}
