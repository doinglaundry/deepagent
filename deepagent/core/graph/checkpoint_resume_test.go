package graph

import (
	"context"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"encoding/json"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"strings"
	"testing"
)

func TestCheckpoint_PartialBatchPreservesBlockedAndPendingStates(t *testing.T) {
	ctx := context.Background()
	first, approval, last := &namedCountingTool{name: "first"}, &namedCountingTool{name: "approval"}, &namedCountingTool{name: "last"}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{
		{ID: "first", Function: schema.FunctionCall{Name: "first", Arguments: "{}"}},
		{ID: "approval", Function: schema.FunctionCall{Name: "approval", Arguments: "{}"}},
		{ID: "last", Function: schema.FunctionCall{Name: "last", Arguments: "{}"}},
	})}, {schema.AssistantMessage("done", nil)}}}
	cfg := Config{Model: m, RunID: "run", CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.Descriptor{{Tool: first}, {Tool: approval, RequiresApproval: true}, {Tool: last}}}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatal(err)
	}
	check := func(s *types.RunState) {
		t.Helper()
		if s.Phase != types.PhaseBlocked {
			t.Errorf("phase=%s", s.Phase)
		}
		want := []types.CallStatus{types.CallCompleted, types.CallBlocked, types.CallPending}
		if len(s.Calls) != len(want) {
			t.Fatalf("calls=%+v", s.Calls)
		}
		for i, status := range want {
			if s.Calls[i].Status != status {
				t.Errorf("call=%d status=%s want=%s", i, s.Calls[i].Status, status)
			}
		}
	}
	check(a.state)
	if first.count != 1 || approval.count != 0 || last.count != 0 {
		t.Fatal("unexpected side effects before approval")
	}
	cfg.Conversation = a.conversation
	var restored *DeepAgent
	observed := false
	cfg.Emit = func(_ context.Context, event types.RuntimeEvent) error {
		if event.Kind == "run_state_restored" {
			observed = true
			check(restored.state)
		}
		return nil
	}
	restored, err = New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = restored.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "approval", Approved: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if !observed || first.count != 1 || approval.count != 1 || last.count != 1 || restored.state.Phase != types.PhaseCompleted {
		t.Fatalf("observed=%v counts=%d/%d/%d phase=%s", observed, first.count, approval.count, last.count, restored.state.Phase)
	}
}

func TestRun_ResumeContinuesGraphAndModelBudgets(t *testing.T) {
	for _, tc := range []struct {
		name          string
		steps, models int
		wantTools     int32
		wantModel     int
		wantError     string
	}{
		{"exhausted graph", 3, 10, 0, 1, "maximum graph steps"},
		{"one graph step left", 4, 10, 1, 1, "maximum graph steps"},
		{"exhausted model", 20, 1, 1, 1, "maximum model calls"},
		{"enough remaining", 6, 2, 1, 2, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			tool := &countingTool{}
			model := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "approved", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}, {schema.AssistantMessage("done", nil)}}}
			cfg := Config{ThreadID: "thread", RunID: "run", Model: model, CheckpointStore: &checkpointMemory{}, MaxSteps: tc.steps, MaxModelCalls: tc.models, ToolDescriptors: []tools.Descriptor{{Tool: tool, RequiresApproval: true}}}
			first, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			_, err = first.Run(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
			info, ok := compose.ExtractInterruptInfo(err)
			if !ok {
				t.Fatalf("initial run did not interrupt: %v", err)
			}
			if first.state.GraphSteps != 3 || first.state.ModelCalls != 1 {
				t.Fatalf("initial budgets=%+v", first.state)
			}
			cfg.Conversation = first.conversation
			if err = first.Close(ctx); err != nil {
				t.Fatal(err)
			}
			next, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close(ctx)
			out, err := next.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "approved", Approved: true}}))
			if tc.wantError == "" {
				if err != nil || out.Content != "done" {
					t.Fatalf("out=%v err=%v", out, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("wrong budget error: %v", err)
			}
			if tool.count.Load() != tc.wantTools || model.calls != tc.wantModel {
				t.Fatalf("budget exceeded before rejection: tool=%d model=%d", tool.count.Load(), model.calls)
			}
			if next.state.GraphSteps > tc.steps {
				t.Fatalf("checkpoint counter exceeded: %d", next.state.GraphSteps)
			}
		})
	}
}

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
