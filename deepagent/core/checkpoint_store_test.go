package deepagents

import (
	"bytes"
	"context"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"strings"
	"testing"
)

func TestCheckpoint_ForceInitialSaveFailureDoesNotExecuteOrOverwriteOldSnapshot(t *testing.T) {
	ctx := context.Background()
	old := []byte("old checkpoint bytes")
	store := &checkpointMemory{values: map[string][]byte{"checkpoint": old}, fail: true}
	m := &sequenceModel{}
	a, err := NewRun(ctx, WithConfig(&Config{Model: m, RunID: "new-run", CheckpointStore: store}))
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close(ctx)
	_, err = a.Execute(ctx, []*schema.Message{schema.UserMessage("new")}, WithCheckpointID("checkpoint"), WithForceNewRun())
	if err == nil || m.calls != 0 || !bytes.Equal(store.values["checkpoint"], old) {
		t.Fatalf("failed force initialization changed state: err=%v models=%d snapshot=%q", err, m.calls, store.values["checkpoint"])
	}
	{
		_, interrupted := compose.ExtractInterruptInfo(err)
		if interrupted {
			t.Fatal("failed initial write exposed an internal interrupt")
		}
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
			a, err := NewRun(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer a.Close(ctx)
			_, err = a.Execute(ctx, []*schema.Message{schema.UserMessage("go")}, opts...)
			if err != nil {
				t.Fatal(err)
			}

			if counter.count.Load() != 1 || m.calls != 2 || before != 1 || after != 1 || ended != 1 || len(store.fenced) == 0 {
				t.Fatalf("lifecycle/fence: tools=%d models=%d before=%d after=%d end=%d fence=%d", counter.count.Load(), m.calls, before, after, ended, len(store.fenced))
			}
			{
				_, _, err := checkpointer.New(store, "", "run", "core-graph-v1").Get(ctx, "checkpoint")
				if err == nil || !strings.Contains(err.Error(), "terminal") {
					t.Fatalf("successful run did not finalize checkpoint: %v", err)
				}
			}
			cfg.Conversation = a.conversation
			cfg.CheckpointStore = &checkpointMemory{values: map[string][]byte{"checkpoint": store.fenced}}
			replay, err := NewRun(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer replay.Close(ctx)
			_, err = replay.Execute(ctx, nil, WithCheckpointID("checkpoint"))
			if err == nil || !strings.Contains(err.Error(), "unknown outcome") || counter.count.Load() != 1 || m.calls != 2 {
				t.Fatalf("fresh crash replay: %v tools=%d models=%d", err, counter.count.Load(), m.calls)
			}
		})
	}
}

type terminalWriteFailureStore struct {
	checkpointMemory
	failure error
}

func (s *terminalWriteFailureStore) Set(ctx context.Context, id string, raw []byte) error {
	var envelope checkpointer.Envelope
	{
		err := json.Unmarshal(raw, &envelope)
		if err != nil {
			return err
		}
	}
	var snapshot struct {
		MapValues map[string]struct{ JSONValue types.RunState }
	}
	{
		err := json.Unmarshal(envelope.EinoSnapshot, &snapshot)
		if err != nil {
			return err
		}
	}
	if snapshot.MapValues["State"].JSONValue.Phase == types.PhaseCompleted && s.failure != nil {
		return s.failure
	}
	return s.checkpointMemory.Set(ctx, id, raw)
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
			first, err := NewRun(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close(ctx)
			_, err = first.Execute(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
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
			resumed, err := NewRun(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer resumed.Close(ctx)
			_, err = resumed.Execute(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "call", Approved: true}}))
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
			first, err := NewRun(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close(ctx)
			_, err = first.Execute(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
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
			resume, err := NewRun(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer resume.Close(ctx)
			answer := map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "call", Approved: true}}
			_, err = resume.Execute(resumeCtx, nil, WithCheckpointID("checkpoint"), WithResumeData(answer))
			if err == nil || counter.count.Load() != 1 {
				t.Fatalf("failure=%v calls=%d", err, counter.count.Load())
			}
			if canceled && !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel lost: %v", err)
			}
			cfg.Emit = nil
			replay, err := NewRun(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer replay.Close(ctx)
			_, err = replay.Execute(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(answer))
			if err == nil || !strings.Contains(err.Error(), "terminal") || counter.count.Load() != 1 || m.calls != 1 {
				t.Fatalf("terminal checkpoint replayed: err=%v tools=%d models=%d", err, counter.count.Load(), m.calls)
			}
		})
	}
}

type checkpointLifecycle struct {
	middleware.BaseMiddleware
	before, after *int
}

func (m *checkpointLifecycle) BeforeRun(context.Context, *types.RunState) error {
	*m.before++
	return nil
}
func (m *checkpointLifecycle) AfterRun(context.Context, *types.RunState, error) error {
	*m.after++
	return nil
}
