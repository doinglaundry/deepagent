package graph

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	hook "eino-cli/deepagent/core/hooks"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

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
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("new")}, WithCheckpointID("checkpoint"), WithForceNewRun())
	if err == nil || m.calls != 0 || !bytes.Equal(store.values["checkpoint"], old) {
		t.Fatalf("failed force initialization changed state: err=%v models=%d snapshot=%q", err, m.calls, store.values["checkpoint"])
	}
	if _, interrupted := compose.ExtractInterruptInfo(err); interrupted {
		t.Fatal("failed initial write exposed an internal interrupt")
	}
}

func TestCheckpoint_FreshRunCreatesCursorAndFencesBeforeSideEffect(t *testing.T) {
	for _, scenario := range []struct {
		name             string
		streaming, force bool
	}{
		{"Run", false, false}, {"Stream", true, false}, {"ForceRun", false, true}, {"ForceStream", true, true},
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
			cfg := Config{Model: m, RunID: "run", CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: counter}}, Hooks: hook.HooksChain{{BeforeAgent: func(context.Context) error { before++; return nil }, AfterAgent: func(context.Context) error { after++; return nil }}}, Emit: func(_ context.Context, e types.RuntimeEvent) error {
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
			if scenario.streaming {
				sr, err := a.Stream(ctx, []*schema.Message{schema.UserMessage("go")}, opts...)
				if err != nil {
					t.Fatal(err)
				}
				defer sr.Close()
				for {
					_, err = sr.Recv()
					if err == io.EOF {
						break
					}
					if err != nil {
						t.Fatal(err)
					}
				}
			} else {
				_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("go")}, opts...)
				if err != nil {
					t.Fatal(err)
				}
			}
			if counter.count.Load() != 1 || m.calls != 2 || before != 1 || after != 1 || ended != 1 || len(store.fenced) == 0 {
				t.Fatalf("lifecycle/fence: tools=%d models=%d before=%d after=%d end=%d fence=%d", counter.count.Load(), m.calls, before, after, ended, len(store.fenced))
			}
			if _, _, err := checkpointer.New(store, "", "run", "core-graph-v1").Get(ctx, "checkpoint"); err == nil || !strings.Contains(err.Error(), "terminal") {
				t.Fatalf("successful run did not finalize checkpoint: %v", err)
			}
			cfg.Conversation = a.conversation
			cfg.CheckpointStore = &checkpointMemory{values: map[string][]byte{"checkpoint": store.fenced}}
			replay, err := New(ctx, WithConfig(&cfg))
			if err != nil {
				t.Fatal(err)
			}
			defer replay.Close(ctx)
			_, err = replay.Run(ctx, nil, WithCheckpointID("checkpoint"))
			if err == nil || !strings.Contains(err.Error(), "unknown outcome") || counter.count.Load() != 1 || m.calls != 2 {
				t.Fatalf("fresh crash replay: %v tools=%d models=%d", err, counter.count.Load(), m.calls)
			}
		})
	}
}

func TestCheckpoint_EmptyInitialLocalStateMigratesOnResume(t *testing.T) {
	for _, scenario := range []string{"valid", "wrong-run", "executed", "save-failure"} {
		t.Run(scenario, func(t *testing.T) { testEmptyInitialCheckpoint(t, scenario) })
	}
}
func testEmptyInitialCheckpoint(t *testing.T, scenario string) {
	ctx := context.Background()
	store := &checkpointMemory{}
	m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}
	cfg := Config{Model: m, ThreadID: "thread", RunID: "run", CheckpointStore: store, InterruptBeforeNodes: []string{"prepare"}}
	a, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Run(ctx, []*schema.Message{schema.UserMessage("original")}, WithCheckpointID("checkpoint"))
	if _, ok := compose.ExtractInterruptInfo(err); !ok {
		t.Fatal(err)
	}
	// Reproduce the former canonical format: initialized prepare input, empty
	// local State. Preserve every other byte-level checkpoint field.
	raw, _, err := store.Get(ctx, "checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	var envelope checkpointer.Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var root map[string]json.RawMessage
	if err = json.Unmarshal(envelope.EinoSnapshot, &root); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(root["MapValues"], &fields); err != nil {
		t.Fatal(err)
	}
	var state map[string]json.RawMessage
	if err = json.Unmarshal(fields["State"], &state); err != nil {
		t.Fatal(err)
	}
	state["JSONValue"], _ = json.Marshal(types.RunState{})
	fields["State"], _ = json.Marshal(state)
	if scenario == "wrong-run" || scenario == "executed" {
		var inputs map[string]json.RawMessage
		if err = json.Unmarshal(fields["Inputs"], &inputs); err != nil {
			t.Fatal(err)
		}
		var entries map[string]json.RawMessage
		if err = json.Unmarshal(inputs["MapValues"], &entries); err != nil {
			t.Fatal(err)
		}
		var prepare map[string]json.RawMessage
		if err = json.Unmarshal(entries[`"prepare"`], &prepare); err != nil {
			t.Fatal(err)
		}
		var input types.RunState
		if err = json.Unmarshal(prepare["JSONValue"], &input); err != nil {
			t.Fatal(err)
		}
		if scenario == "wrong-run" {
			input.RunID = "another-run"
		} else {
			input.ModelCalls = 1
		}
		prepare["JSONValue"], _ = json.Marshal(input)
		entries[`"prepare"`], _ = json.Marshal(prepare)
		inputs["MapValues"], _ = json.Marshal(entries)
		fields["Inputs"], _ = json.Marshal(inputs)
	}
	root["MapValues"], _ = json.Marshal(fields)
	envelope.EinoSnapshot, _ = json.Marshal(root)
	raw, _ = json.Marshal(envelope)
	if err = store.Set(ctx, "checkpoint", raw); err != nil {
		t.Fatal(err)
	}
	if scenario == "save-failure" {
		store.fail = true
	}
	cfg.InterruptBeforeNodes = nil
	resumed, err := New(ctx, WithConfig(&cfg))
	if err != nil {
		t.Fatal(err)
	}
	out, err := resumed.Run(ctx, nil, WithCheckpointID("checkpoint"))
	if scenario != "valid" {
		if err == nil || m.calls != 0 {
			t.Fatalf("unsafe migration: err=%v modelCalls=%d", err, m.calls)
		}
		stored, _, _ := store.Get(ctx, "checkpoint")
		if string(stored) != string(raw) {
			t.Fatal("rejected migration overwrote checkpoint")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "done" || m.calls != 1 || resumed.state.RunID != "run" || resumed.state.ThreadID != "thread" || len(resumed.state.Consumed) != 1 || resumed.state.Consumed[0].Message.Content != "original" {
		t.Fatalf("out=%v calls=%d state=%+v", out, m.calls, resumed.state)
	}
}
