package checkpointer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/compose"
)

func TestCheckpointValidatesIdentityAndRoundTripsSnapshot(t *testing.T) {
	ctx := context.Background()
	storage := &memoryStore{}
	store := NewGraphStore(storage, "thread", "run")
	err := store.Set(ctx, "checkpoint", newPreservedSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, exists, err := store.Get(ctx, "checkpoint")
	if err != nil || !exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
	runID, err := ReadRunID(snapshot, "thread")
	if err != nil || runID != "run" {
		t.Fatalf("run=%q err=%v", runID, err)
	}
	readSavedSnapshotFields(t, snapshot)
	for _, other := range []*GraphStore{NewGraphStore(storage, "other", "run"), NewGraphStore(storage, "thread", "other")} {
		_, _, err := other.Get(ctx, "checkpoint")
		if err == nil {
			t.Fatal("accepted another Run's checkpoint")
		}
	}
}

func TestCheckpointStoreFailurePropagates(t *testing.T) {
	sentinel := errors.New("storage unavailable")
	store := NewGraphStore(&memoryStore{err: sentinel}, "thread", "run")
	setErr := store.Set(context.Background(), "id", newPreservedSnapshot(t))
	if !errors.Is(setErr, sentinel) {
		t.Fatal(setErr)
	}
}

func TestFreshCheckpointReplacesForeignIdentityAndInvalidBytes(t *testing.T) {
	snapshot := []byte(`{"MapValues":{"State":{"Type":{"SimpleType":"deepagent_run_state_v1"},"JSONValue":{"Version":1,"ThreadID":"thread","RunID":"fresh","Phase":"preparing"}}}}`)
	for _, existing := range [][]byte{[]byte(`invalid bytes`), newPreservedSnapshot(t)} {
		storage := &memoryStore{data: map[string][]byte{"checkpoint": existing}}
		store := NewGraphStore(storage, "thread", "fresh")
		err := store.Set(context.Background(), "checkpoint", snapshot)
		if err != nil {
			t.Fatal(err)
		}
		restored, exists, err := store.Get(context.Background(), "checkpoint")
		if err != nil || !exists {
			t.Fatalf("exists=%v err=%v", exists, err)
		}
		runID, err := ReadRunID(restored, "thread")
		if err != nil || runID != "fresh" {
			t.Fatalf("run=%q err=%v", runID, err)
		}
	}
}

func TestAppendInputsPreservesCheckpointFieldsAndInputCursor(t *testing.T) {
	state := types.RunState{Version: 1, ThreadID: "thread", RunID: "run", Phase: types.PhaseBlocked, PreparedInputs: 1, Consumed: []types.Input{{Message: messagepkg.NewUserMessage("original")}}, Extensions: map[string]json.RawMessage{"middleware:test": json.RawMessage(`{"value":3}`)}}
	encoded, _ := json.Marshal(state)
	snapshot := []byte(`{"Type":{"PointerNum":1,"StructType":"_eino_checkpoint"},"future":"preserved","MapValues":{"State":{"Type":{"PointerNum":1,"SimpleType":"deepagent_run_state_v1"},"JSONValue":` + string(encoded) + `},"InterruptID2Addr":{"untouched":"address"},"Inputs":{"untouched":"node input"}}}`)
	original := snapshot
	rawStore := &memoryStore{data: map[string][]byte{"checkpoint": original}}
	input := types.Input{Message: messagepkg.NewUserMessage("pending"), Meta: map[string]string{"MessageID": "9007199254740993"}}
	appendInputsErr := AppendInputs(context.Background(), rawStore, "checkpoint", "thread", "run", []types.Input{input})
	if appendInputsErr != nil {
		t.Fatal(appendInputsErr)
	}
	var root, fields, value map[string]json.RawMessage
	rootDecodeErr := json.Unmarshal(rawStore.data["checkpoint"], &root)
	if rootDecodeErr != nil {
		t.Fatal(rootDecodeErr)
	}
	if string(root["future"]) != `"preserved"` {
		t.Fatal("unknown snapshot field lost")
	}
	fieldsDecodeErr := json.Unmarshal(root["MapValues"], &fields)
	if fieldsDecodeErr != nil {
		t.Fatal(fieldsDecodeErr)
	}
	if string(fields["InterruptID2Addr"]) != `{"untouched":"address"}` || string(fields["Inputs"]) != `{"untouched":"node input"}` {
		t.Fatal("graph execution position changed")
	}
	valueDecodeErr := json.Unmarshal(fields["State"], &value)
	if valueDecodeErr != nil {
		t.Fatal(valueDecodeErr)
	}
	decodeErr := json.Unmarshal(value["JSONValue"], &state)
	if decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(state.Consumed) != 2 || state.PreparedInputs != 1 || string(state.Extensions["middleware:test"]) != `{"value":3}` {
		t.Fatalf("state=%+v", state)
	}
	if state.Consumed[1].Meta.(map[string]string)["MessageID"] != "9007199254740993" {
		t.Fatal("typed metadata changed")
	}
	for _, testCase := range []struct{ name, thread, run string }{{"thread", "other", "run"}, {"run", "thread", "other"}} {
		t.Run(testCase.name, func(t *testing.T) {
			rawStore.data["checkpoint"] = original
			err := AppendInputs(context.Background(), rawStore, "checkpoint", testCase.thread, testCase.run, []types.Input{input})
			if err == nil {
				t.Fatal("foreign checkpoint accepted")
			}
			if string(rawStore.data["checkpoint"]) != string(original) {
				t.Fatal("foreign checkpoint overwritten")
			}
		})
	}
}

func TestCheckpointMutationsPreserveUnknownWireFields(t *testing.T) {
	for _, operation := range []string{"append", "fence", "pending", "finalize"} {
		t.Run(operation, func(t *testing.T) {
			snapshot := newPreservedSnapshot(t)
			rawStore := &memoryStore{data: map[string][]byte{"checkpoint": snapshot}}
			store := NewGraphStore(rawStore, "thread", "run")
			ctx := context.Background()
			var err error
			switch operation {
			case "append":
				err = AppendInputs(ctx, rawStore, "checkpoint", "thread", "run", []types.Input{{MessageID: "pending", Message: messagepkg.NewUserMessage("pending")}})
			case "fence":
				err = store.MarkToolOutcomeUnknown(ctx, "checkpoint", types.ToolCall{ID: "call", Name: "write", Arguments: "{}"}, true)
			case "pending":
				err = store.SaveInterrupts(ctx, "checkpoint", []types.Interrupt{{InterruptID: "interrupt", CheckpointID: "checkpoint"}})
			case "finalize":
				var root struct {
					MapValues map[string]struct{ JSONValue types.RunState }
				}
				err = json.Unmarshal(snapshot, &root)
				if err != nil {
					t.Fatal(err)
				}
				state := root.MapValues["State"].JSONValue
				state.Phase = types.PhaseCompleted
				err = store.SaveTerminalState(ctx, "checkpoint", &state, true)
			}
			if err != nil {
				t.Fatal(err)
			}
			state := readSavedSnapshotFields(t, rawStore.data["checkpoint"])
			var calls []map[string]json.RawMessage
			err = json.Unmarshal(state["Calls"], &calls)
			if err != nil {
				t.Fatal(err)
			}
			if len(calls) != 1 || string(calls[0]["FutureCall"]) != `{"kept":true}` {
				t.Fatalf("persisted tool-call field lost: %s", state["Calls"])
			}
			var call map[string]json.RawMessage
			err = json.Unmarshal(calls[0]["Call"], &call)
			if err != nil {
				t.Fatal(err)
			}
			if string(call["FutureIdentity"]) != `"identity"` {
				t.Fatal("tool identity field lost")
			}
			if operation == "finalize" && string(state["Phase"]) != `"completed"` {
				t.Fatal("terminal phase normalized to blocked")
			}
			if string(state["PreparedInputs"]) != `1` {
				t.Fatal("prepared cursor changed")
			}
		})
	}
}

func TestCheckpointRejectsInvalidRunState(t *testing.T) {
	for _, invalid := range []struct{ name, field, value string }{
		{"negative input cursor", "PreparedInputs", "-1"},
		{"input cursor beyond messages", "PreparedInputs", "2"},
		{"unsupported format", "Version", "2"},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			snapshotState, err := decodeSnapshot(newPreservedSnapshot(t))
			if err != nil {
				t.Fatal(err)
			}
			snapshotState.runStateFields[invalid.field] = json.RawMessage(invalid.value)
			snapshot, err := snapshotState.encodeSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			storage := &memoryStore{data: map[string][]byte{"checkpoint": snapshot}}
			store := NewGraphStore(storage, "thread", "run")
			_, _, err = store.Get(context.Background(), "checkpoint")
			if err == nil {
				t.Fatal("invalid RunState resumed")
			}
			err = store.Set(context.Background(), "checkpoint", snapshot)
			if err == nil {
				t.Fatal("invalid RunState saved")
			}
			if string(storage.data["checkpoint"]) != string(snapshot) {
				t.Fatal("invalid checkpoint was mutated")
			}
		})
	}
}

func TestGraphStoreRejectsMissingOrForeignRunState(t *testing.T) {
	for _, snapshot := range []string{
		`{}`, `{"MapValues":{}}`,
		`{"MapValues":{"State":{"Type":{"SimpleType":"other"},"JSONValue":{"Phase":"modeling"}}}}`,
		`{"MapValues":{"State":{"Type":{"SimpleType":"deepagent_run_state_v1"},"JSONValue":null}}}`,
	} {
		storage := &memoryStore{data: map[string][]byte{"checkpoint": []byte(snapshot)}}
		store := NewGraphStore(storage, "thread", "run")
		_, _, err := store.Get(context.Background(), "checkpoint")
		if err == nil {
			t.Fatal("invalid RunState resumed")
		}
		err = store.Set(context.Background(), "checkpoint", []byte(snapshot))
		if err == nil {
			t.Fatal("invalid RunState saved")
		}
		if string(storage.data["checkpoint"]) != snapshot {
			t.Fatal("invalid checkpoint was mutated")
		}
	}
}

func TestToolFencesShareSnapshotButResumeRejectsUnknownOutcomes(t *testing.T) {
	raw := newPreservedSnapshot(t)
	rawStore := &memoryStore{data: map[string][]byte{"checkpoint": raw}}
	store := NewGraphStore(rawStore, "thread", "run")
	ctx := context.Background()
	for _, id := range []string{"first", "second"} {
		err := store.MarkToolOutcomeUnknown(ctx, "checkpoint", types.ToolCall{ID: id, Name: "write", Arguments: "{}"}, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	_, _, err := store.Get(ctx, "checkpoint")
	if err == nil {
		t.Fatal("unknown side effect was resumable")
	}
	state := readSavedSnapshotFields(t, rawStore.data["checkpoint"])
	var calls []types.ToolCallState
	err = json.Unmarshal(state["Calls"], &calls)
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || calls[1].Status != types.CallOutcomeUnknown || calls[2].Status != types.CallOutcomeUnknown {
		t.Fatalf("calls=%+v", calls)
	}
	err = AppendInputs(ctx, rawStore, "checkpoint", "thread", "run", []types.Input{{Message: messagepkg.NewUserMessage("accepted before interruption")}})
	if err != nil {
		t.Fatal("accepted input must be sealed even when side-effect outcome requires reconciliation", err)
	}
}

func TestInterruptMetadataRejectsUnknownToolOutcome(t *testing.T) {
	raw := newPreservedSnapshot(t)
	storage := &memoryStore{data: map[string][]byte{"checkpoint": raw}}
	store := NewGraphStore(storage, "thread", "run")
	ctx := context.Background()
	err := store.MarkToolOutcomeUnknown(ctx, "checkpoint", types.ToolCall{ID: "call"}, true)
	if err != nil {
		t.Fatal(err)
	}
	before := string(storage.data["checkpoint"])
	err = store.SaveInterrupts(ctx, "checkpoint", []types.Interrupt{{InterruptID: "interrupt", CheckpointID: "checkpoint"}})
	var unknownOutcome *ToolOutcomeUnknownError
	isUnknownOutcome := errors.As(err, &unknownOutcome)
	if !isUnknownOutcome {
		t.Fatalf("expected unknown tool outcome, got %v", err)
	}
	if unknownOutcome.CallID != "call" || len(unknownOutcome.Inputs) != 1 || unknownOutcome.Inputs[0].MessageID != "original" {
		t.Fatalf("lost call identity or accepted inputs: %+v", unknownOutcome)
	}
	if string(storage.data["checkpoint"]) != before {
		t.Fatal("rejected interrupt update changed the checkpoint")
	}
}

func TestTerminalCheckpointCannotResumeOrAcceptInputs(t *testing.T) {
	snapshot := newPreservedSnapshot(t)
	rawStore := &memoryStore{data: map[string][]byte{"checkpoint": snapshot}}
	store := NewGraphStore(rawStore, "thread", "run")
	_, state, err := decodeActiveSnapshot(snapshot, "thread", "run")
	if err != nil {
		t.Fatal(err)
	}
	state.Phase = types.PhaseInterrupted
	err = store.SaveTerminalState(context.Background(), "checkpoint", state, true)
	if err != nil {
		t.Fatal(err)
	}
	before := string(rawStore.data["checkpoint"])
	_, _, err = store.Get(context.Background(), "checkpoint")
	if err == nil {
		t.Fatal("terminal checkpoint resumed")
	}
	for name, mutate := range map[string]func() error{
		"append": func() error {
			return AppendInputs(context.Background(), rawStore, "checkpoint", "thread", "run", []types.Input{{Message: messagepkg.NewUserMessage("late")}})
		},
		"interrupts": func() error {
			return store.SaveInterrupts(context.Background(), "checkpoint", []types.Interrupt{{InterruptID: "interrupt", CheckpointID: "checkpoint"}})
		},
		"tool outcome": func() error {
			return store.MarkToolOutcomeUnknown(context.Background(), "checkpoint", types.ToolCall{ID: "late"}, true)
		},
		"terminal state": func() error {
			return store.SaveTerminalState(context.Background(), "checkpoint", state, true)
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := mutate()
			if err == nil || string(rawStore.data["checkpoint"]) != before {
				t.Fatal("terminal checkpoint mutated")
			}
		})
	}
}

func TestSaveTerminalStateReplacesKnownOptionalToolResultFields(t *testing.T) {
	snapshot := newPreservedSnapshot(t)
	snapshotState, _, err := decodeActiveSnapshot(snapshot, "thread", "run")
	if err != nil {
		t.Fatal(err)
	}
	var calls []map[string]json.RawMessage
	err = json.Unmarshal(snapshotState.runStateFields["Calls"], &calls)
	if err != nil {
		t.Fatal(err)
	}
	calls[0]["Result"] = json.RawMessage(`{"CallID":"call","Content":"old","MultiContent":[{"Type":"text","Text":"old"}],"FutureResult":true}`)
	snapshotState.runStateFields["Calls"], err = json.Marshal(calls)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = snapshotState.encodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	state, err := snapshotState.decodeActiveRunState()
	if err != nil {
		t.Fatal(err)
	}
	state.Phase = types.PhaseCompleted
	state.Calls[0].Result.Content = "new"
	state.Calls[0].Result.MultiContent = nil
	rawStore := &memoryStore{data: map[string][]byte{"checkpoint": snapshot}}
	err = NewGraphStore(rawStore, "thread", "run").SaveTerminalState(context.Background(), "checkpoint", state, true)
	if err != nil {
		t.Fatal(err)
	}
	fields := readSavedSnapshotFields(t, rawStore.data["checkpoint"])
	err = json.Unmarshal(fields["Calls"], &calls)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	err = json.Unmarshal(calls[0]["Result"], &result)
	if err != nil {
		t.Fatal(err)
	}
	_, stale := result["MultiContent"]
	if stale || string(result["Content"]) != `"new"` || string(result["FutureResult"]) != "true" {
		t.Fatalf("result=%s", calls[0]["Result"])
	}
}

// A checkpoint written by Eino must be readable without a second wrapper.
func TestGraphStoreResumesEinoSnapshotFromFile(t *testing.T) {
	ctx := context.Background()
	storage, err := NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	state := &types.RunState{Version: 1, ThreadID: "thread", RunID: "run", Phase: types.PhasePreparing}
	build := func(storage compose.CheckPointStore) compose.Runnable[*types.RunState, *types.RunState] {
		graph := compose.NewGraph[*types.RunState, *types.RunState](compose.WithGenLocalState(func(context.Context) *types.RunState { return state }))
		err := graph.AddLambdaNode("work", compose.InvokableLambda(func(ctx context.Context, _ *types.RunState) (*types.RunState, error) {
			var restored *types.RunState
			err := compose.ProcessState[*types.RunState](ctx, func(_ context.Context, localState *types.RunState) error { restored = localState; return nil })
			return restored, err
		}))
		if err != nil {
			t.Fatal(err)
		}
		err = graph.AddEdge(compose.START, "work")
		if err != nil {
			t.Fatal(err)
		}
		err = graph.AddEdge("work", compose.END)
		if err != nil {
			t.Fatal(err)
		}
		runnable, err := graph.Compile(ctx, compose.WithCheckPointStore(storage), compose.WithInterruptBeforeNodes([]string{"work"}))
		if err != nil {
			t.Fatal(err)
		}
		return runnable
	}
	original := build(storage)
	_, err = original.Invoke(ctx, state, compose.WithCheckPointID("checkpoint"))
	_, interrupted := compose.ExtractInterruptInfo(err)
	if !interrupted {
		t.Fatalf("expected checkpoint interrupt, got %v", err)
	}
	restored := build(NewGraphStore(storage, "thread", "run"))
	result, err := restored.Invoke(ctx, nil, compose.WithCheckPointID("checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	if result.ThreadID != "thread" || result.RunID != "run" {
		t.Fatalf("lost Run identity: %+v", result)
	}
}

func TestGraphStoreRejectsForeignStateBeforeWriting(t *testing.T) {
	snapshot := newPreservedSnapshot(t)
	original := snapshot
	storage := &memoryStore{data: map[string][]byte{"checkpoint": original}}
	store := NewGraphStore(storage, "other", "run")
	err := store.Set(context.Background(), "checkpoint", snapshot)
	if err == nil {
		t.Fatal("foreign RunState was accepted")
	}
	if string(storage.data["checkpoint"]) != string(original) {
		t.Fatal("rejected write changed checkpoint")
	}
}

type memoryStore struct {
	data map[string][]byte
	err  error
}

func (memoryStore *memoryStore) Get(_ context.Context, id string) ([]byte, bool, error) {
	snapshot, ok := memoryStore.data[id]
	return snapshot, ok, memoryStore.err
}

func (memoryStore *memoryStore) Set(_ context.Context, id string, value []byte) error {
	if memoryStore.err != nil {
		return memoryStore.err
	}
	if memoryStore.data == nil {
		memoryStore.data = make(map[string][]byte)
	}
	memoryStore.data[id] = value
	return nil
}

func newPreservedSnapshot(t *testing.T) []byte {
	t.Helper()
	state := types.RunState{Version: 1, ThreadID: "thread", RunID: "run", Phase: types.PhaseBlocked, Consumed: []types.Input{{MessageID: "original", Message: messagepkg.NewUserMessage("original"), Meta: map[string]string{"MessageID": "9007199254740993"}}}}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	err = json.Unmarshal(encoded, &fields)
	if err != nil {
		t.Fatal(err)
	}
	fields["PreparedInputs"] = json.RawMessage(`1`)
	fields["FutureState"] = json.RawMessage(`{"kept":true}`)
	fields["Calls"] = json.RawMessage(`[{"Call":{"ID":"call","Name":"write","Arguments":"{}","FutureIdentity":"identity"},"Status":"completed","Result":{"CallID":"call","Content":"done"},"FutureCall":{"kept":true}}]`)
	encoded, err = json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := []byte(`{"FutureRoot":{"kept":true},"MapValues":{"FutureField":{"kept":true},"Inputs":{"untouched":"node input"},"InterruptID2Addr":{"MapValues":{"\"interrupt\"":{}}},"State":{"FutureWrapper":{"kept":true},"Type":{"SimpleType":"deepagent_run_state_v1","FutureType":"type"},"JSONValue":` + string(encoded) + `}}}`)
	return snapshot
}

func readSavedSnapshotFields(t *testing.T, snapshot []byte) map[string]json.RawMessage {
	t.Helper()
	var root, fields, wrapper, state map[string]json.RawMessage
	err := json.Unmarshal(snapshot, &root)
	if err != nil {
		t.Fatal(err)
	}
	err = json.Unmarshal(root["MapValues"], &fields)
	if err != nil {
		t.Fatal(err)
	}
	err = json.Unmarshal(fields["State"], &wrapper)
	if err != nil {
		t.Fatal(err)
	}
	err = json.Unmarshal(wrapper["JSONValue"], &state)
	if err != nil {
		t.Fatal(err)
	}
	for _, retainedField := range []struct {
		name string
		raw  json.RawMessage
	}{
		{"root", root["FutureRoot"]},
		{"field", fields["FutureField"]},
		{"wrapper", wrapper["FutureWrapper"]},
		{"state", state["FutureState"]},
	} {
		if string(retainedField.raw) != `{"kept":true}` {
			t.Errorf("%s field was lost: %s", retainedField.name, retainedField.raw)
		}
	}
	if string(fields["Inputs"]) != `{"untouched":"node input"}` {
		t.Fatal("Eino execution input changed")
	}
	if string(fields["InterruptID2Addr"]) != `{"MapValues":{"\"interrupt\"":{}}}` {
		t.Fatal("Eino interrupt address changed")
	}
	return state
}
