package checkpointer

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"
)

func TestCheckpointValidatesIdentityAndRoundTripsSnapshot(t *testing.T) {
	ctx := context.Background()
	raw := &memoryStore{}
	store := NewGraphStore(raw, "thread", "run", "graph-v1")
	setErr := store.Set(ctx, "checkpoint", []byte("eino snapshot"))
	if setErr != nil {
		t.Fatal(setErr)
	}
	got, ok, err := store.Get(ctx, "checkpoint")
	if err != nil || !ok || string(got) != "eino snapshot" {
		t.Fatalf("%s %v %v", got, ok, err)
	}
	for _, other := range []*GraphStore{NewGraphStore(raw, "other", "run", "graph-v1"), NewGraphStore(raw, "thread", "other", "graph-v1"), NewGraphStore(raw, "thread", "run", "graph-v2")} {
		_, _, err := other.Get(ctx, "checkpoint")
		if err == nil {
			t.Fatal("accepted incompatible checkpoint")
		}
	}
}

func TestCheckpointStoreFailurePropagates(t *testing.T) {
	sentinel := errors.New("storage unavailable")
	store := NewGraphStore(&memoryStore{err: sentinel}, "thread", "run", "v1")
	setErr := store.Set(context.Background(), "id", []byte("snapshot"))
	if !errors.Is(setErr, sentinel) {
		t.Fatal(setErr)
	}
}

func TestFreshCheckpointReplacesForeignIdentityAndInvalidBytes(t *testing.T) {
	for _, existing := range [][]byte{
		[]byte(`obsolete bytes`),
		[]byte(`{"Version":1,"ThreadID":"thread","RunID":"old","GraphVersion":"graph-v1","EinoSnapshot":"b2xk","OldOwnerField":true}`),
	} {
		rawStore := &memoryStore{data: map[string][]byte{"checkpoint": existing}}
		store := NewGraphStore(rawStore, "thread", "fresh", "graph-v1")
		err := store.Set(context.Background(), "checkpoint", []byte("fresh snapshot"))
		if err != nil {
			t.Fatal(err)
		}
		got, exists, err := store.Get(context.Background(), "checkpoint")
		if err != nil || !exists || string(got) != "fresh snapshot" {
			t.Fatalf("snapshot=%s exists=%v err=%v", got, exists, err)
		}
		var checkpoint map[string]json.RawMessage
		err = json.Unmarshal(rawStore.data["checkpoint"], &checkpoint)
		if err != nil {
			t.Fatal(err)
		}
		_, oldField := checkpoint["OldOwnerField"]
		if oldField {
			t.Fatal("foreign execution metadata retained")
		}
	}
}

func TestAppendInputsPreservesCheckpointFieldsAndInputCursor(t *testing.T) {
	state := types.RunState{Version: 1, ThreadID: "thread", RunID: "run", Phase: types.PhaseBlocked, PreparedInputs: 1, Consumed: []types.Input{{Message: messagepkg.NewUserMessage("original")}}, Extensions: map[string]json.RawMessage{"middleware:test": json.RawMessage(`{"value":3}`)}}
	encoded, _ := json.Marshal(state)
	snapshot := []byte(`{"Type":{"PointerNum":1,"StructType":"_eino_checkpoint"},"future":"preserved","MapValues":{"State":{"Type":{"PointerNum":1,"SimpleType":"deepagent_run_state_v1"},"JSONValue":` + string(encoded) + `},"InterruptID2Addr":{"untouched":"address"},"Inputs":{"untouched":"node input"}}}`)
	checkpoint := Checkpoint{Version: 1, ThreadID: "thread", RunID: "run", GraphVersion: "core-graph-v1", Snapshot: snapshot}
	original, _ := json.Marshal(checkpoint)
	rawStore := &memoryStore{data: map[string][]byte{"checkpoint": original}}
	input := types.Input{Message: messagepkg.NewUserMessage("pending"), Meta: map[string]string{"MessageID": "9007199254740993"}}
	appendInputsErr := AppendInputs(context.Background(), rawStore, "checkpoint", "thread", "run", []types.Input{input})
	if appendInputsErr != nil {
		t.Fatal(appendInputsErr)
	}
	var saved Checkpoint
	savedDecodeErr := json.Unmarshal(rawStore.data["checkpoint"], &saved)
	if savedDecodeErr != nil {
		t.Fatal(savedDecodeErr)
	}
	var root, fields, value map[string]json.RawMessage
	rootDecodeErr := json.Unmarshal(saved.Snapshot, &root)
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
			raw, snapshot := newPreservedCheckpoint(t)
			rawStore := &memoryStore{data: map[string][]byte{"checkpoint": raw}}
			store := NewGraphStore(rawStore, "thread", "run", "core-graph-v1")
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
			_, state := readSavedCheckpointFields(t, rawStore.data["checkpoint"])
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

func TestCheckpointRejectsInvalidPreparedCursor(t *testing.T) {
	for _, cursor := range []string{"-1", "2"} {
		t.Run(cursor, func(t *testing.T) {
			raw, snapshot := newPreservedCheckpoint(t)
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
			state["PreparedInputs"] = json.RawMessage(cursor)
			wrapper["JSONValue"], err = json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			fields["State"], err = json.Marshal(wrapper)
			if err != nil {
				t.Fatal(err)
			}
			root["MapValues"], err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err = json.Marshal(root)
			if err != nil {
				t.Fatal(err)
			}
			var checkpoint Checkpoint
			err = json.Unmarshal(raw, &checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			checkpoint.Snapshot = snapshot
			raw, err = json.Marshal(checkpoint)
			if err != nil {
				t.Fatal(err)
			}
			rawStore := &memoryStore{data: map[string][]byte{"checkpoint": raw}}
			store := NewGraphStore(rawStore, "thread", "run", "core-graph-v1")
			_, _, err = store.Get(context.Background(), "checkpoint")
			if err == nil {
				t.Fatal("invalid input cursor accepted")
			}
			if string(rawStore.data["checkpoint"]) != string(raw) {
				t.Fatal("invalid checkpoint was mutated")
			}
		})
	}
}

func TestBlockedNormalizationLeavesForeignStateUntouched(t *testing.T) {
	for _, snapshot := range []string{
		`{"MapValues":{}}`,
		`{"MapValues":{"State":{"Type":{"SimpleType":"other"},"JSONValue":{"Phase":"modeling"}}}}`,
	} {
		got, err := markSnapshotBlocked([]byte(snapshot))
		if err != nil || string(got) != snapshot {
			t.Fatalf("snapshot=%s err=%v", got, err)
		}
	}
	_, err := markSnapshotBlocked([]byte(`{"MapValues":{"State":{"Type":{"SimpleType":"deepagent_run_state_v1"},"JSONValue":null}}}`))
	if err == nil {
		t.Fatal("missing canonical state accepted")
	}
}

func TestToolFencesShareSnapshotButResumeRejectsUnknownOutcomes(t *testing.T) {
	raw, _ := newPreservedCheckpoint(t)
	rawStore := &memoryStore{data: map[string][]byte{"checkpoint": raw}}
	store := NewGraphStore(rawStore, "thread", "run", "core-graph-v1")
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
	_, state := readSavedCheckpointFields(t, rawStore.data["checkpoint"])
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
	raw, _ := newPreservedCheckpoint(t)
	storage := &memoryStore{data: map[string][]byte{"checkpoint": raw}}
	store := NewGraphStore(storage, "thread", "run", "core-graph-v1")
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
	raw, snapshot := newPreservedCheckpoint(t)
	rawStore := &memoryStore{data: map[string][]byte{"checkpoint": raw}}
	store := NewGraphStore(rawStore, "thread", "run", "core-graph-v1")
	_, state, err := decodeActiveSnapshot(snapshot)
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
	raw, snapshot := newPreservedCheckpoint(t)
	snapshotState, _, err := decodeActiveSnapshot(snapshot)
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
	var checkpoint map[string]json.RawMessage
	err = json.Unmarshal(raw, &checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint["EinoSnapshot"], err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(checkpoint)
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
	rawStore := &memoryStore{data: map[string][]byte{"checkpoint": raw}}
	err = NewGraphStore(rawStore, "thread", "run", "core-graph-v1").SaveTerminalState(context.Background(), "checkpoint", state, true)
	if err != nil {
		t.Fatal(err)
	}
	_, fields := readSavedCheckpointFields(t, rawStore.data["checkpoint"])
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

func newPreservedCheckpoint(t *testing.T) ([]byte, []byte) {
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
	raw, err := json.Marshal(Checkpoint{Version: 1, ThreadID: "thread", RunID: "run", GraphVersion: "core-graph-v1", Snapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint map[string]json.RawMessage
	err = json.Unmarshal(raw, &checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint["FutureEnvelope"] = json.RawMessage(`{"kept":true}`)
	raw, err = json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return raw, snapshot
}

func readSavedCheckpointFields(t *testing.T, raw []byte) (map[string]json.RawMessage, map[string]json.RawMessage) {
	t.Helper()
	var checkpoint map[string]json.RawMessage
	err := json.Unmarshal(raw, &checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot []byte
	err = json.Unmarshal(checkpoint["EinoSnapshot"], &snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var root, fields, wrapper, state map[string]json.RawMessage
	err = json.Unmarshal(snapshot, &root)
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
		{"checkpoint", checkpoint["FutureEnvelope"]},
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
	return checkpoint, state
}
