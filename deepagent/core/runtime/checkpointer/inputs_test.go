package checkpointer

import (
	"context"
	"eino-cli/deepagent/core/types"
	"encoding/json"
	"github.com/cloudwego/eino/schema"
	"testing"
)

func TestAppendInputsPreservesCheckpointFieldsAndInputCursor(t *testing.T) {
	state := types.RunState{Version: 1, ThreadID: "thread", RunID: "run", Phase: types.PhaseBlocked, Consumed: []types.Input{{Message: schema.UserMessage("original")}}, Extensions: map[string]json.RawMessage{"prepared_inputs": json.RawMessage(`1`), "middleware:test": json.RawMessage(`{"value":3}`)}}
	encoded, _ := json.Marshal(state)
	snapshot := []byte(`{"Type":{"PointerNum":1,"StructType":"_eino_checkpoint"},"future":"preserved","MapValues":{"State":{"Type":{"PointerNum":1,"SimpleType":"deepagent_run_state_v1"},"JSONValue":` + string(encoded) + `},"InterruptID2Addr":{"untouched":"address"},"Inputs":{"untouched":"node input"}}}`)
	envelope := Envelope{Version: 1, ThreadID: "thread", RunID: "run", GraphVersion: "core-graph-v1", EinoSnapshot: snapshot}
	original, _ := json.Marshal(envelope)
	inner := &memoryStore{data: map[string][]byte{"checkpoint": original}}
	input := types.Input{Message: schema.UserMessage("pending"), Meta: map[string]string{"MessageID": "9007199254740993"}}
	if err := AppendInputs(context.Background(), inner, "checkpoint", "thread", "run", []types.Input{input}); err != nil {
		t.Fatal(err)
	}
	var saved Envelope
	if err := json.Unmarshal(inner.data["checkpoint"], &saved); err != nil {
		t.Fatal(err)
	}
	var root, fields, value map[string]json.RawMessage
	if err := json.Unmarshal(saved.EinoSnapshot, &root); err != nil {
		t.Fatal(err)
	}
	if string(root["future"]) != `"preserved"` {
		t.Fatal("unknown snapshot field lost")
	}
	if err := json.Unmarshal(root["MapValues"], &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["InterruptID2Addr"]) != `{"untouched":"address"}` || string(fields["Inputs"]) != `{"untouched":"node input"}` {
		t.Fatal("graph execution position changed")
	}
	if err := json.Unmarshal(fields["State"], &value); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(value["JSONValue"], &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Consumed) != 2 || string(state.Extensions["prepared_inputs"]) != "1" || string(state.Extensions["middleware:test"]) != `{"value":3}` || string(state.Extensions["pending_inputs"]) != "true" {
		t.Fatalf("state=%+v", state)
	}
	if state.Consumed[1].Meta.(map[string]string)["MessageID"] != "9007199254740993" {
		t.Fatal("typed metadata changed")
	}
	for _, tc := range []struct{ name, thread, run string }{{"thread", "other", "run"}, {"run", "thread", "other"}} {
		t.Run(tc.name, func(t *testing.T) {
			inner.data["checkpoint"] = original
			if err := AppendInputs(context.Background(), inner, "checkpoint", tc.thread, tc.run, []types.Input{input}); err == nil {
				t.Fatal("foreign checkpoint accepted")
			}
			if string(inner.data["checkpoint"]) != string(original) {
				t.Fatal("foreign checkpoint overwritten")
			}
		})
	}
}
