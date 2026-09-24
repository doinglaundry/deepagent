package graph

import (
	"context"
	"encoding/json"
	"testing"

	"eino-cli/deepagent/core/runtime/checkpointer"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestRun_ReusingAgentGeneratesDistinctRunIDs(t *testing.T) {
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("one", nil)}, {schema.AssistantMessage("two", nil)}}}
	a, err := New(context.Background(), WithModel(m))
	if err != nil {
		t.Fatal(err)
	}
	var previous string
	for range 2 {
		if _, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("go")}); err != nil {
			t.Fatal(err)
		}
		if a.state.RunID == "" || a.state.RunID == previous {
			t.Fatalf("run ID reused: %q", a.state.RunID)
		}
		previous = a.state.RunID
	}
}

func TestCheckpoint_AutomaticRunIdentityRestoresOnNewAgent(t *testing.T) {
	ctx := context.Background()
	store := &checkpointMemory{}
	tool := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	cfg := Config{Model: m, ThreadID: "thread", CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: tool, RequiresApproval: true}}}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatal(err)
	}
	original := a.state.RunID
	var envelope checkpointer.Envelope
	if err := json.Unmarshal(store.values["checkpoint"], &envelope); err != nil {
		t.Fatal(err)
	}
	if original == "" || envelope.RunID != original {
		t.Fatalf("state=%q envelope=%q", original, envelope.RunID)
	}
	cfg.Conversation = a.conversation
	restored, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	out, err := restored.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{Approved: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "done" || restored.state.RunID != original || tool.count.Load() != 1 {
		t.Fatalf("out=%v run=%q tool=%d", out, restored.state.RunID, tool.count.Load())
	}
	cfg.ThreadID = "different-thread"
	foreign, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreign.Run(ctx, nil, WithCheckpointID("checkpoint")); err == nil {
		t.Fatal("restored another thread's checkpoint")
	}
	if m.calls != 2 || tool.count.Load() != 1 {
		t.Fatal("identity rejection performed work")
	}
}

func TestCheckpoint_ForceNewRunDoesNotReuseSuspendedIdentity(t *testing.T) {
	ctx := context.Background()
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
		{schema.AssistantMessage("fresh", nil)},
	}}
	tool := &countingTool{}
	a, err := New(ctx, WithConfig(&Config{Model: m, ThreadID: "thread", CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.Descriptor{{Tool: tool, RequiresApproval: true}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("old")}, WithCheckpointID("checkpoint"))
	if _, ok := compose.ExtractInterruptInfo(err); !ok {
		t.Fatal(err)
	}
	original := a.state.RunID
	out, err := a.Run(ctx, []*schema.Message{schema.UserMessage("new")}, WithCheckpointID("checkpoint"), WithForceNewRun())
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "fresh" || a.state.RunID == original || a.state.ModelCalls != 1 || tool.count.Load() != 0 {
		t.Fatalf("out=%v state=%+v tool=%d", out, a.state, tool.count.Load())
	}
}
