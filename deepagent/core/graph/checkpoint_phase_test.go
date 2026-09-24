package graph

import (
	"context"
	"fmt"
	"testing"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
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

func TestCheckpoint_ExternalNodeInterruptPersistsBlockedPhase(t *testing.T) {
	for _, node := range []string{"prepare", "model"} {
		for _, before := range []bool{false, true} {
			t.Run(fmt.Sprintf("node=%s/before=%v", node, before), func(t *testing.T) {
				ctx := context.Background()
				m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}
				cfg := Config{Model: m, CheckpointStore: &checkpointMemory{}}
				if before {
					cfg.InterruptBeforeNodes = []string{node}
				} else {
					cfg.InterruptAfterNodes = []string{node}
				}
				a, err := New(ctx, WithConfig(&cfg))
				if err != nil {
					t.Fatal(err)
				}
				_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
				if _, ok := compose.ExtractInterruptInfo(err); !ok {
					t.Fatalf("missing interrupt: %v", err)
				}
				cfg.Conversation = a.conversation
				cfg.InterruptBeforeNodes = nil
				cfg.InterruptAfterNodes = nil
				var resumed *DeepAgent
				restored := false
				cfg.Emit = func(_ context.Context, e types.RuntimeEvent) error {
					if e.Kind == "run_state_restored" {
						restored = true
						if resumed.state.Phase != types.PhaseBlocked {
							t.Errorf("persisted phase=%s", resumed.state.Phase)
						}
					}
					return nil
				}
				resumed, err = New(ctx, WithConfig(&cfg))
				if err != nil {
					t.Fatal(err)
				}
				out, err := resumed.Run(ctx, nil, WithCheckpointID("checkpoint"))
				if err != nil {
					t.Fatal(err)
				}
				if len(resumed.state.Consumed) != 1 || resumed.state.Consumed[0].Message.Content != "go" {
					t.Fatal("original input lost")
				}
				if !restored || out.Content != "done" || m.calls != 1 {
					t.Fatalf("restored=%v out=%v calls=%d", restored, out, m.calls)
				}
			})
		}
	}

}
