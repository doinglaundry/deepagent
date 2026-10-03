package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	checkpointer "eino-cli/deepagent/graph/checkpoint"
	"eino-cli/deepagent/graph/middleware"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"

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
	cfg := Config{Model: m, RunID: "run", CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.ToolDescriptor{{Tool: first}, {Tool: approval, RequiresApproval: true}, {Tool: last}}}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
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
	var restored *Graph
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
	_, err = restored.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "approval", Approved: true}}))
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
			cfg := Config{ThreadID: "thread", RunID: "run", Model: model, CheckpointStore: &checkpointMemory{}, MaxSteps: tc.steps, MaxModelCalls: tc.models, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, RequiresApproval: true}}}
			first, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			_, err = first.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
			info, ok := compose.ExtractInterruptInfo(err)
			if !ok {
				t.Fatalf("initial run did not interrupt: %v", err)
			}
			if first.state.GraphSteps != 3 || first.state.ModelCalls != 1 {
				t.Fatalf("initial budgets=%+v", first.state)
			}
			cfg.Conversation = first.conversation
			err = first.Close(ctx)
			if err != nil {
				t.Fatal(err)
			}
			next, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer next.Close(ctx)
			out, err := next.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "approved", Approved: true}}))
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

func TestRun_NewAgentsGenerateDistinctRunIDs(t *testing.T) {
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("one", nil)}, {schema.AssistantMessage("two", nil)}}}
	var previous string
	for range 2 {
		a, err := New(context.Background(), WithModel(m))
		if err != nil {
			t.Fatal(err)
		}
		_, err = a.Invoke(context.Background(), []*schema.Message{schema.UserMessage("go")})
		if err != nil {
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
	resource := &resourceMiddleware{name: "resource"}
	cfg := Config{Model: m, Middlewares: []middleware.Middleware{resource}, ThreadID: "thread", CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, RequiresApproval: true}}}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatal(err)
	}
	original := a.state.RunID
	var envelope checkpointer.Envelope
	decodeErr := json.Unmarshal(store.values["checkpoint"], &envelope)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if original == "" || envelope.RunID != original {
		t.Fatalf("state=%q envelope=%q", original, envelope.RunID)
	}
	cfg.Conversation = a.conversation
	restored, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	out, err := restored.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{Approved: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if resource.closed != 2 {
		t.Fatalf("resume rebuilt resources: closed=%d", resource.closed)
	}
	if out.Content != "done" || restored.state.RunID != original || tool.count.Load() != 1 {
		t.Fatalf("out=%v run=%q tool=%d", out, restored.state.RunID, tool.count.Load())
	}
	cfg.ThreadID = "different-thread"
	foreign, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, executeErr := foreign.Invoke(ctx, nil, WithCheckpointID("checkpoint"))
	if executeErr == nil {
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
	a, err := New(ctx, WithConfig(&Config{Model: m, ThreadID: "thread", CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, RequiresApproval: true}}}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Invoke(ctx, []*schema.Message{schema.UserMessage("old")}, WithCheckpointID("checkpoint"))
	_, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatal(err)
	}
	original := a.state.RunID
	cfg := a.cfg
	cfg.Conversation = a.conversation
	a, err = New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	out, err := a.Invoke(ctx, []*schema.Message{schema.UserMessage("new")}, WithCheckpointID("checkpoint"), WithForceNewRun())
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "fresh" || a.state.RunID == original || a.state.ModelCalls != 1 || tool.count.Load() != 0 {
		t.Fatalf("out=%v state=%+v tool=%d", out, a.state, tool.count.Load())
	}
}

func TestCheckpoint_ForceInitialSaveFailureDoesNotExecuteOrOverwriteOldSnapshot(t *testing.T) {
	ctx := context.Background()
	old := []byte("old checkpoint bytes")
	store := &checkpointMemory{values: map[string][]byte{"checkpoint": old}, fail: true}
	m := &sequenceModel{}
	a, err := New(ctx, WithConfig(&Config{Model: m, RunID: "new-run", CheckpointStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	_, err = a.Invoke(ctx, []*schema.Message{schema.UserMessage("new")}, WithCheckpointID("checkpoint"), WithForceNewRun())
	if err == nil || m.calls != 0 || !bytes.Equal(store.values["checkpoint"], old) {
		t.Fatalf("failed force initialization changed state: err=%v models=%d snapshot=%q", err, m.calls, store.values["checkpoint"])
	}
	_, interrupted := compose.ExtractInterruptInfo(err)
	if interrupted {
		t.Fatal("failed initial write exposed an internal interrupt")
	}
}

func TestCheckpoint_FreshRunCreatesCursorAndFencesBeforeSideEffect(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		force bool
	}{
		{"Run", false}, {"ForceRun", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			store := &executionFenceStore{}
			opts := []RunOptionFunc{WithCheckpointID("checkpoint")}
			if scenario.force {
				store.values = map[string][]byte{"checkpoint": []byte("obsolete checkpoint must not be read")}
				opts = append(opts, WithForceNewRun())
			}
			counter := &countingTool{}
			m := &sequenceModel{responses: [][]*schema.Message{
				{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
				{schema.AssistantMessage("done", nil)},
			}}
			before, after, ended := 0, 0, 0
			cfg := Config{Model: m, RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: counter}}, Middlewares: []middleware.Middleware{&checkpointLifecycle{before: &before, after: &after}}, Emit: func(_ context.Context, e types.RuntimeEvent) error {
				if e.Kind == "turn_end" {
					ended++
				}
				return nil
			}}
			a, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(ctx)
			_, err = a.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, opts...)
			if err != nil {
				t.Fatal(err)
			}

			if counter.count.Load() != 1 || m.calls != 2 || before != 1 || after != 1 || ended != 1 || len(store.fenced) == 0 {
				t.Fatalf("lifecycle/fence: tools=%d models=%d before=%d after=%d end=%d fence=%d", counter.count.Load(), m.calls, before, after, ended, len(store.fenced))
			}
			_, _, getErr := checkpointer.New(store, "", "run", "core-graph-v1").Get(ctx, "checkpoint")
			if getErr == nil || !strings.Contains(getErr.Error(), "terminal") {
				t.Fatalf("successful run did not finalize checkpoint: %v", getErr)
			}
			cfg.Conversation = a.conversation
			cfg.CheckpointStore = &checkpointMemory{values: map[string][]byte{"checkpoint": store.fenced}}
			replay, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer replay.Close(ctx)
			_, err = replay.Invoke(ctx, nil, WithCheckpointID("checkpoint"))
			if err == nil || !strings.Contains(err.Error(), "unknown outcome") || counter.count.Load() != 1 || m.calls != 2 {
				t.Fatalf("fresh crash replay: %v tools=%d models=%d", err, counter.count.Load(), m.calls)
			}
		})
	}
}

func TestCheckpoint_TerminalStorageFailureDoesNotPublishSuccess(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "write rejected"
		if missing {
			name = "checkpoint disappeared"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := &terminalWriteFailureStore{}
			counter := &countingTool{}
			m := &sequenceModel{responses: [][]*schema.Message{
				{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
				{schema.AssistantMessage("done", nil)},
			}}
			cfg := Config{Model: m, RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: counter, RequiresApproval: true}}}
			first, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close(ctx)
			_, err = first.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
			info, ok := compose.ExtractInterruptInfo(err)
			if !ok {
				t.Fatal(err)
			}
			cfg.Conversation = first.conversation
			failure := errors.New("terminal write rejected")
			turnEnds := 0
			cfg.Emit = func(_ context.Context, event types.RuntimeEvent) error {
				if event.Kind == "tool_end" {
					if missing {
						delete(store.values, "checkpoint")
					} else {
						store.failure = failure
					}
				}
				if event.Kind == "turn_end" {
					turnEnds++
				}
				return nil
			}
			resumed, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Close(ctx)
			_, err = resumed.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "call", Approved: true}}))
			if err == nil || turnEnds != 0 || counter.count.Load() != 1 || resumed.state.Phase != types.PhaseFailed {
				t.Fatalf("false success: err=%v ends=%d tools=%d phase=%s", err, turnEnds, counter.count.Load(), resumed.state.Phase)
			}
			if missing {
				if !strings.Contains(err.Error(), "disappeared") {
					t.Fatal(err)
				}
			} else {
				if !errors.Is(err, failure) {
					t.Fatalf("lost store failure: %v", err)
				}
				_, _, err = checkpointer.New(store, "", "run", "core-graph-v1").Get(ctx, "checkpoint")
				if err == nil || !strings.Contains(err.Error(), "unknown outcome") {
					t.Fatalf("failed terminal write lost fence: %v", err)
				}
			}
		})
	}
}

func TestCheckpoint_FailedOrCanceledResumeCannotReplayApprovedTool(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		name := "model budget failure"
		if canceled {
			name = "caller cancellation"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			counter := &countingTool{}
			m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
			cfg := Config{Model: m, RunID: "run", MaxModelCalls: 1, CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.ToolDescriptor{{Tool: counter, RequiresApproval: true}}}
			first, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close(ctx)
			_, err = first.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
			info, ok := compose.ExtractInterruptInfo(err)
			if !ok {
				t.Fatal(err)
			}
			cfg.Conversation = first.conversation
			resumeCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			if canceled {
				cfg.Emit = func(_ context.Context, event types.RuntimeEvent) error {
					if event.Kind == "tool_end" {
						cancel()
					}
					return nil
				}
			}
			resume, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer resume.Close(ctx)
			answer := map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "call", Approved: true}}
			_, err = resume.Invoke(resumeCtx, nil, WithCheckpointID("checkpoint"), WithResumeData(answer))
			if err == nil || counter.count.Load() != 1 {
				t.Fatalf("failure=%v calls=%d", err, counter.count.Load())
			}
			if canceled && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel lost: %v", err)
			}
			cfg.Emit = nil
			replay, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer replay.Close(ctx)
			_, err = replay.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(answer))
			if err == nil || !strings.Contains(err.Error(), "terminal") || counter.count.Load() != 1 || m.calls != 1 {
				t.Fatalf("terminal checkpoint replayed: err=%v tools=%d models=%d", err, counter.count.Load(), m.calls)
			}
		})
	}
}

func TestCheckpoint_PendingApprovalSurvivesPolicyChange(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
		{schema.AssistantMessage("done", nil)},
	}}
	cfg := Config{Model: m, RunID: "run", CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, RequiresApproval: true}}}
	first, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(ctx)
	_, err = first.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok || len(info.InterruptContexts) != 1 {
		t.Fatalf("interrupt=%+v err=%v", info, err)
	}
	if len(first.state.Pending) != 1 || first.state.Pending[0].InterruptID != info.InterruptContexts[0].ID || first.state.Pending[0].CallID != "call" || first.state.Pending[0].CheckpointID != "checkpoint" {
		t.Errorf("pending=%+v", first.state.Pending)
	}
	cfg.Conversation = first.conversation
	cfg.ToolDescriptors[0].RequiresApproval = false
	second, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close(ctx)
	_, err = second.Invoke(ctx, nil, WithCheckpointID("checkpoint"))
	again, ok := compose.ExtractInterruptInfo(err)
	if !ok || tool.count.Load() != 0 {
		t.Fatalf("approval bypassed: calls=%d err=%v", tool.count.Load(), err)
	}
	third, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close(ctx)
	_, err = third.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{again.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "call", Approved: true}}))
	if err != nil {
		t.Fatal(err)
	}
	if tool.count.Load() != 1 || len(third.state.Pending) != 0 {
		t.Fatalf("calls=%d pending=%+v", tool.count.Load(), third.state.Pending)
	}
}

func TestCheckpoint_PendingWriteFailureCannotLoseApprovalObligation(t *testing.T) {
	ctx := context.Background()
	tool := &countingTool{}
	store := &pendingWriteFailure{failure: errors.New("pending metadata write failed")}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
	cfg := Config{Model: m, RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: tool, RequiresApproval: true}}}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	_, err = a.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	if !errors.Is(err, store.failure) {
		t.Fatalf("failure lost: %v", err)
	}
	_, blocked := compose.ExtractInterruptInfo(err)
	if blocked {
		t.Fatal("failed metadata write published a blocked result")
	}
	cfg.Conversation = a.conversation
	cfg.ToolDescriptors[0].RequiresApproval = false
	resumed, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close(ctx)
	_, err = resumed.Invoke(ctx, nil, WithCheckpointID("checkpoint"))
	if err == nil || !strings.Contains(err.Error(), "terminal") || tool.count.Load() != 0 {
		t.Fatalf("failed run remained resumable: calls=%d err=%v", tool.count.Load(), err)
	}
	// If the process dies before terminal cleanup, the original Eino write
	// must still retain the approval obligation without metadata enrichment.
	cfg.CheckpointStore = &checkpointMemory{values: map[string][]byte{"checkpoint": store.initial}}
	crashResume, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer crashResume.Close(ctx)
	_, err = crashResume.Invoke(ctx, nil, WithCheckpointID("checkpoint"))
	_, interruptBlocked := compose.ExtractInterruptInfo(err)
	if !interruptBlocked || tool.count.Load() != 0 {
		t.Fatalf("initial snapshot lost approval obligation: calls=%d err=%v", tool.count.Load(), err)
	}
}

func TestCheckpoint_CompletedApprovalRemovedBeforeNextInterruptSnapshot(t *testing.T) {
	for _, approved := range []bool{true, false} {
		for _, answerCallID := range []string{"first", ""} {
			t.Run(fmt.Sprintf("approved=%t/answerCallID=%s", approved, answerCallID), func(t *testing.T) {
				ctx := context.Background()
				counter := &countingTool{}
				store := &pendingSnapshotStore{}
				m := &sequenceModel{responses: [][]*schema.Message{
					{schema.AssistantMessage("", []schema.ToolCall{
						{ID: "first", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}},
						{ID: "second", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}},
					})},
					{schema.AssistantMessage("done", nil)},
				}}
				cfg := Config{Model: m, RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: counter, RequiresApproval: true}}}
				a, err := New(ctx, WithConfig(&cfg))
				if err != nil {
					t.Fatal(err)
				}
				defer a.Close(ctx)
				_, err = a.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
				info, ok := compose.ExtractInterruptInfo(err)
				if !ok || len(info.InterruptContexts) != 1 {
					t.Fatalf("first interrupt: %v", err)
				}
				cfg.Conversation = a.conversation
				resumed, err := New(ctx, WithConfig(&cfg))
				if err != nil {
					t.Fatal(err)
				}
				defer resumed.Close(ctx)
				before := len(store.writes)
				_, err = resumed.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{
					info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: answerCallID, Approved: approved},
				}))
				expectedCalls := int32(0)
				if approved {
					expectedCalls = 1
				}
				second, ok := compose.ExtractInterruptInfo(err)
				if !ok || len(second.InterruptContexts) != 1 || counter.count.Load() != expectedCalls {
					t.Fatalf("second interrupt: %v calls=%d", err, counter.count.Load())
				}
				if len(store.writes) <= before {
					t.Fatal("no checkpoint saved")
				}
				var envelope checkpointer.Envelope
				if approved {
					// The execution fence precedes Eino's next interrupt snapshot.
					before++
				}
				if len(store.writes) <= before {
					t.Fatal("no interrupt snapshot after execution fence")
				}
				envelopeDecodeErr := json.Unmarshal(store.writes[before], &envelope)
				if envelopeDecodeErr != nil {
					t.Fatal(envelopeDecodeErr)
				}
				var snapshot struct {
					MapValues map[string]struct{ JSONValue types.RunState }
				}
				decodeErr := json.Unmarshal(envelope.EinoSnapshot, &snapshot)
				if decodeErr != nil {
					t.Fatal(decodeErr)
				}
				state := snapshot.MapValues["State"].JSONValue
				if len(state.Pending) != 1 || state.Pending[0].CallID != "second" {
					t.Fatalf("original Eino snapshot retained resolved approval: %+v", state.Pending)
				}
				if state.Calls[0].Status != types.CallCompleted || state.Calls[1].Status != types.CallBlocked {
					t.Fatalf("calls=%+v", state.Calls)
				}
				cfg.Conversation = resumed.conversation
				last, err := New(ctx, WithConfig(&cfg))
				if err != nil {
					t.Fatal(err)
				}
				defer last.Close(ctx)
				_, err = last.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{
					second.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "second", Approved: true},
				}))
				if err != nil || counter.count.Load() != expectedCalls+1 || len(last.state.Pending) != 0 {
					t.Fatalf("final err=%v calls=%d pending=%+v", err, counter.count.Load(), last.state.Pending)
				}

			})
		}
	}
}

func TestRun_FollowUpArgumentAliasesPreserveQuestionAndResume(t *testing.T) {
	for _, raw := range []string{
		`{"question":" Choose ","context":" Detail ","options":[" yes ","","no"]}`,
		`{"prompt":" Choose ","context":" Detail ","options":"[\" yes \",\"\",\"no\"]"}`,
		`{"message":" Choose ","context":" Detail ","options":["yes","no"]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			ctx := context.Background()
			m := &sequenceModel{responses: [][]*schema.Message{
				{schema.AssistantMessage("", []schema.ToolCall{{ID: "question", Function: schema.FunctionCall{Name: "ask_user", Arguments: raw}}})},
				{schema.AssistantMessage("done", nil)},
			}}
			var observed *schema.Message
			emit := func(_ context.Context, e types.RuntimeEvent) error {
				if e.Kind == "llm_end" {
					payload := e.Data.(types.LLMEnd)
					observed = payload.Message
				}
				return nil
			}
			cfg := Config{Emit: emit, Model: m, RunID: "run", CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.ToolDescriptor{tools.GetFollowUpTool()}}
			a, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(ctx)
			_, err = a.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
			info, ok := compose.ExtractInterruptInfo(err)
			if !ok || len(info.InterruptContexts) != 1 {
				t.Fatalf("interrupt=%+v err=%v", info, err)
			}
			if observed == nil || len(observed.ToolCalls) != 1 || observed.ToolCalls[0].Function.Arguments != raw {
				t.Fatalf("event lost original clarification call: %+v", observed)
			}
			question, ok := info.InterruptContexts[0].Info.(*tools.FollowUpInfo)
			if !ok || question.Question != "Detail\n\nChoose" || !reflect.DeepEqual(question.Questions, []string{"yes", "no"}) {
				t.Fatalf("question=%+v", question)
			}
			cfg.Conversation = a.conversation
			resumed, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Close(ctx)
			result, err := resumed.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.FollowUpInfo{UserAnswer: "yes"}}))
			if err != nil || result == nil || result.Content != "done" {
				t.Fatalf("result=%v err=%v", result, err)
			}
			found := false
			for _, message := range resumed.conversation.History(ctx) {
				if message.Role == schema.Tool && message.ToolCallID == "question" && message.Content == "yes" {
					found = true
				}
			}
			if !found {
				t.Fatal("resume answer missing from conversation")
			}
		})
	}
}

func TestCheckpoint_ExecutionFencePreventsCrashReplayAndGatesTool(t *testing.T) {
	for _, failWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "crash snapshot", true: "write failure"}[failWrite], func(t *testing.T) {
			ctx := context.Background()
			store := &executionFenceStore{}
			counter := &countingTool{}
			m := &sequenceModel{responses: [][]*schema.Message{
				{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})},
				{schema.AssistantMessage("done", nil)},
			}}
			cfg := Config{Model: m, RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: counter, RequiresApproval: true}}}
			first, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close(ctx)
			_, err = first.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
			info, ok := compose.ExtractInterruptInfo(err)
			if !ok {
				t.Fatal(err)
			}
			failure := errors.New("fence write failed")
			if failWrite {
				store.failure = failure
			}
			cfg.Conversation = first.conversation
			resumed, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Close(ctx)
			answer := map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "call", Approved: true}}
			_, err = resumed.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(answer), func(o *RunOptions) { o.WriteToCheckpointID = "checkpoint" })
			if failWrite {
				if !errors.Is(err, failure) || counter.count.Load() != 0 {
					t.Fatalf("fence failure bypassed: %v calls=%d", err, counter.count.Load())
				}
				return
			}
			if err != nil || counter.count.Load() != 1 || len(store.fenced) == 0 {
				t.Fatalf("missing fence: err=%v count=%d", err, counter.count.Load())
			}
			// Restore the durable bytes from immediately before the side effect,
			// as if the process died before any result or terminal write.
			cfg.CheckpointStore = &checkpointMemory{values: map[string][]byte{"checkpoint": store.fenced}}
			replay, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer replay.Close(ctx)
			_, err = replay.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(answer))
			if err == nil || !strings.Contains(err.Error(), "unknown outcome") || counter.count.Load() != 1 || m.calls != 2 {
				t.Fatalf("crash replay: err=%v tools=%d models=%d", err, counter.count.Load(), m.calls)
			}
		})
	}
}

func TestCheckpoint_ParallelFencesRetainEveryCall(t *testing.T) {
	ctx := context.Background()
	store := &executionFenceStore{}
	counter := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{
		{schema.AssistantMessage("", []schema.ToolCall{
			{ID: "first", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}},
			{ID: "second", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}},
		})},
		{schema.AssistantMessage("done", nil)},
	}}
	cfg := Config{Model: m, RunID: "run", CheckpointStore: store, Parallelism: 2, ToolDescriptors: []tools.ToolDescriptor{{Tool: counter, ParallelSafe: true}}}
	first, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(ctx)
	first.cfg.Emit = func(_ context.Context, event types.RuntimeEvent) error {
		if event.Kind == "llm_end" {
			first.Interrupt()
		}
		return nil
	}
	_, err = first.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	_, ok := compose.ExtractInterruptInfo(err)
	if !ok || counter.count.Load() != 0 {
		t.Fatalf("initial interrupt: %v", err)
	}
	cfg.Conversation = first.conversation
	cfg.Emit = nil
	resumed, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close(ctx)
	_, err = resumed.Invoke(ctx, nil, WithCheckpointID("checkpoint"))
	if err != nil || counter.count.Load() != 2 {
		t.Fatalf("parallel resume: %v calls=%d", err, counter.count.Load())
	}
	var envelope checkpointer.Envelope
	envelopeDecodeErr := json.Unmarshal(store.fenced, &envelope)
	if envelopeDecodeErr != nil {
		t.Fatal(envelopeDecodeErr)
	}
	var snapshot struct {
		MapValues map[string]struct{ JSONValue types.RunState }
	}
	decodeErr := json.Unmarshal(envelope.EinoSnapshot, &snapshot)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	calls := snapshot.MapValues["State"].JSONValue.Calls
	if len(calls) != 2 {
		t.Fatalf("lost parallel call: %+v", calls)
	}
	for _, call := range calls {
		if call.Status != types.CallOutcomeUnknown {
			t.Fatalf("lost execution fence: %+v", call)
		}
	}
	cfg.CheckpointStore = &checkpointMemory{values: map[string][]byte{"checkpoint": store.fenced}}
	replay, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close(ctx)
	_, err = replay.Invoke(ctx, nil, WithCheckpointID("checkpoint"))
	if err == nil || !strings.Contains(err.Error(), "unknown outcome") || counter.count.Load() != 2 || m.calls != 2 {
		t.Fatalf("parallel crash replay: err=%v tools=%d models=%d", err, counter.count.Load(), m.calls)
	}
}

func TestCheckpoint_DisappearingRestoredSnapshotPreventsToolExecution(t *testing.T) {
	ctx := context.Background()
	store := &checkpointMemory{}
	counter := &countingTool{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "counter", Arguments: "{}"}}})}}}
	cfg := Config{Model: m, RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: counter, RequiresApproval: true}}}
	first, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close(ctx)
	_, err = first.Invoke(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		t.Fatal(err)
	}
	cfg.Conversation = first.conversation
	removed := false
	cfg.Emit = func(_ context.Context, event types.RuntimeEvent) error {
		if event.Kind == "run_state_restored" {
			// Eino has decoded the saved execution cursor; storage now loses it.
			delete(store.values, "checkpoint")
			removed = true
		}
		return nil
	}
	resumed, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close(ctx)
	_, err = resumed.Invoke(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "call", Approved: true}}))
	if !removed || err == nil || !strings.Contains(err.Error(), "disappeared") || counter.count.Load() != 0 || m.calls != 1 {
		t.Fatalf("lost checkpoint bypassed fence: removed=%v err=%v tools=%d models=%d", removed, err, counter.count.Load(), m.calls)
	}
}
