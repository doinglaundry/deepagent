package graph

import (
	"context"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type terminalWriteFailureStore struct {
	checkpointMemory
	failure error
}

func (s *terminalWriteFailureStore) Set(ctx context.Context, id string, raw []byte) error {
	var envelope checkpointer.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	var snapshot struct {
		MapValues map[string]struct{ JSONValue types.RunState }
	}
	if err := json.Unmarshal(envelope.EinoSnapshot, &snapshot); err != nil {
		return err
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
			cfg := Config{Model: m, RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: counter, RequiresApproval: true}}}
			first, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close(ctx)
			_, err = first.Run(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
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
			_, err = resumed.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "call", Approved: true}}))
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
			cfg := Config{Model: m, RunID: "run", MaxModelCalls: 1, CheckpointStore: &checkpointMemory{}, ToolDescriptors: []tools.Descriptor{{Tool: counter, RequiresApproval: true}}}
			first, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer first.Close(ctx)
			_, err = first.Run(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
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
			_, err = resume.Run(resumeCtx, nil, WithCheckpointID("checkpoint"), WithResumeData(answer))
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
			_, err = replay.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(answer))
			if err == nil || !strings.Contains(err.Error(), "terminal") || counter.count.Load() != 1 || m.calls != 1 {
				t.Fatalf("terminal checkpoint replayed: err=%v tools=%d models=%d", err, counter.count.Load(), m.calls)
			}
		})
	}
}
