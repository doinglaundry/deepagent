package graph

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"eino-cli/deepagent/core/runtime/checkpointer"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type executionFenceStore struct {
	checkpointMemory
	fenced  []byte
	failure error
}

func (s *executionFenceStore) Set(ctx context.Context, id string, raw []byte) error {
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
	for _, call := range snapshot.MapValues["State"].JSONValue.Calls {
		if call.Status == types.CallOutcomeUnknown {
			s.fenced = append([]byte(nil), raw...)
			if s.failure != nil {
				return s.failure
			}
		}
	}
	return s.checkpointMemory.Set(ctx, id, raw)
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
			_, err = first.Run(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
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
			_, err = resumed.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(answer), func(o *RunOptions) { o.WriteToCheckpointID = "checkpoint" })
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
			_, err = replay.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(answer))
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
	first.emit = func(_ context.Context, event types.RuntimeEvent) error {
		if event.Kind == "llm_end" {
			first.Interrupt()
		}
		return nil
	}
	_, err = first.Run(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
	if _, ok := compose.ExtractInterruptInfo(err); !ok || counter.count.Load() != 0 {
		t.Fatalf("initial interrupt: %v", err)
	}
	cfg.Conversation = first.conversation
	cfg.Emit = nil
	resumed, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close(ctx)
	_, err = resumed.Run(ctx, nil, WithCheckpointID("checkpoint"))
	if err != nil || counter.count.Load() != 2 {
		t.Fatalf("parallel resume: %v calls=%d", err, counter.count.Load())
	}
	var envelope checkpointer.Envelope
	if err := json.Unmarshal(store.fenced, &envelope); err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		MapValues map[string]struct{ JSONValue types.RunState }
	}
	if err := json.Unmarshal(envelope.EinoSnapshot, &snapshot); err != nil {
		t.Fatal(err)
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
	_, err = replay.Run(ctx, nil, WithCheckpointID("checkpoint"))
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
	_, err = first.Run(ctx, []*schema.Message{schema.UserMessage("go")}, WithCheckpointID("checkpoint"))
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
	_, err = resumed.Run(ctx, nil, WithCheckpointID("checkpoint"), WithResumeData(map[string]any{info.InterruptContexts[0].ID: &tools.ApprovalResult{CallID: "call", Approved: true}}))
	if !removed || err == nil || !strings.Contains(err.Error(), "disappeared") || counter.count.Load() != 0 || m.calls != 1 {
		t.Fatalf("lost checkpoint bypassed fence: removed=%v err=%v tools=%d models=%d", removed, err, counter.count.Load(), m.calls)
	}
}
