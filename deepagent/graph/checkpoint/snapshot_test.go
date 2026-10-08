package checkpointer

import (
	"context"
	"encoding/json"
	"testing"

	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"
)

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
	raw, err := json.Marshal(Envelope{Version: 1, ThreadID: "thread", RunID: "run", GraphVersion: "core-graph-v1", EinoSnapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	err = json.Unmarshal(raw, &envelope)
	if err != nil {
		t.Fatal(err)
	}
	envelope["FutureEnvelope"] = json.RawMessage(`{"kept":true}`)
	raw, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return raw, snapshot
}

func readSavedCheckpointFields(t *testing.T, raw []byte) (map[string]json.RawMessage, map[string]json.RawMessage) {
	t.Helper()
	var envelope map[string]json.RawMessage
	err := json.Unmarshal(raw, &envelope)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot []byte
	err = json.Unmarshal(envelope["EinoSnapshot"], &snapshot)
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
		{"envelope", envelope["FutureEnvelope"]},
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
	return envelope, state
}

func TestCheckpointMutationsPreserveUnknownWireFields(t *testing.T) {
	for _, operation := range []string{"append", "fence", "pending", "finalize"} {
		t.Run(operation, func(t *testing.T) {
			raw, snapshot := newPreservedCheckpoint(t)
			rawStore := &memoryStore{data: map[string][]byte{"checkpoint": raw}}
			store := New(rawStore, "thread", "run", "core-graph-v1")
			ctx := context.Background()
			var err error
			switch operation {
			case "append":
				err = AppendInputs(ctx, rawStore, "checkpoint", "thread", "run", []types.Input{{MessageID: "pending", Message: messagepkg.NewUserMessage("pending")}})
			case "fence":
				err = store.FenceTool(ctx, "checkpoint", types.ToolCall{ID: "call", Name: "write", Arguments: "{}"}, true)
			case "pending":
				err = store.SavePending(ctx, "checkpoint", []types.Interrupt{{InterruptID: "interrupt", CheckpointID: "checkpoint"}})
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
				err = store.Finalize(ctx, "checkpoint", &state, true)
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
			var envelope Envelope
			err = json.Unmarshal(raw, &envelope)
			if err != nil {
				t.Fatal(err)
			}
			envelope.EinoSnapshot = snapshot
			raw, err = json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			rawStore := &memoryStore{data: map[string][]byte{"checkpoint": raw}}
			store := New(rawStore, "thread", "run", "core-graph-v1")
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

func TestToolFencesShareSnapshotButResumeRejectsUnknownOutcomes(t *testing.T) {
	raw, _ := newPreservedCheckpoint(t)
	rawStore := &memoryStore{data: map[string][]byte{"checkpoint": raw}}
	store := New(rawStore, "thread", "run", "core-graph-v1")
	ctx := context.Background()
	for _, id := range []string{"first", "second"} {
		err := store.FenceTool(ctx, "checkpoint", types.ToolCall{ID: id, Name: "write", Arguments: "{}"}, true)
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

func TestTerminalCheckpointCannotResumeOrAcceptInputs(t *testing.T) {
	raw, snapshot := newPreservedCheckpoint(t)
	rawStore := &memoryStore{data: map[string][]byte{"checkpoint": raw}}
	store := New(rawStore, "thread", "run", "core-graph-v1")
	snapshotState, err := requireSnapshotState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	state, err := snapshotState.decodeRunState()
	if err != nil {
		t.Fatal(err)
	}
	state.Phase = types.PhaseInterrupted
	err = store.Finalize(context.Background(), "checkpoint", state, true)
	if err != nil {
		t.Fatal(err)
	}
	before := string(rawStore.data["checkpoint"])
	_, _, err = store.Get(context.Background(), "checkpoint")
	if err == nil {
		t.Fatal("terminal checkpoint resumed")
	}
	err = AppendInputs(context.Background(), rawStore, "checkpoint", "thread", "run", []types.Input{{Message: messagepkg.NewUserMessage("late")}})
	if err == nil || string(rawStore.data["checkpoint"]) != before {
		t.Fatal("terminal checkpoint mutated")
	}
}

func TestFinalizeReplacesKnownOptionalToolResultFields(t *testing.T) {
	raw, snapshot := newPreservedCheckpoint(t)
	snapshotState, err := requireSnapshotState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var calls []map[string]json.RawMessage
	err = json.Unmarshal(snapshotState.value["Calls"], &calls)
	if err != nil {
		t.Fatal(err)
	}
	calls[0]["Result"] = json.RawMessage(`{"CallID":"call","Content":"old","MultiContent":[{"Type":"text","Text":"old"}],"FutureResult":true}`)
	snapshotState.value["Calls"], err = json.Marshal(calls)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err = snapshotState.encodeSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]json.RawMessage
	err = json.Unmarshal(raw, &envelope)
	if err != nil {
		t.Fatal(err)
	}
	envelope["EinoSnapshot"], err = json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	state, err := snapshotState.decodeRunState()
	if err != nil {
		t.Fatal(err)
	}
	state.Phase = types.PhaseCompleted
	state.Calls[0].Result.Content = "new"
	state.Calls[0].Result.MultiContent = nil
	rawStore := &memoryStore{data: map[string][]byte{"checkpoint": raw}}
	err = New(rawStore, "thread", "run", "core-graph-v1").Finalize(context.Background(), "checkpoint", state, true)
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
