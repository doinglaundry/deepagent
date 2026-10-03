package checkpointer

import (
	"context"
	"encoding/json"
	"testing"

	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/schema"
)

func TestAppendInputsPreservesCheckpointFieldsAndInputCursor(t *testing.T) {
	state := types.RunState{Version: 1, ThreadID: "thread", RunID: "run", Phase: types.PhaseBlocked, PreparedInputs: 1, Consumed: []types.Input{{Message: schema.UserMessage("original")}}, Extensions: map[string]json.RawMessage{"middleware:test": json.RawMessage(`{"value":3}`)}}
	encoded, _ := json.Marshal(state)
	snapshot := []byte(`{"Type":{"PointerNum":1,"StructType":"_eino_checkpoint"},"future":"preserved","MapValues":{"State":{"Type":{"PointerNum":1,"SimpleType":"deepagent_run_state_v1"},"JSONValue":` + string(encoded) + `},"InterruptID2Addr":{"untouched":"address"},"Inputs":{"untouched":"node input"}}}`)
	envelope := Envelope{Version: 1, ThreadID: "thread", RunID: "run", GraphVersion: "core-graph-v1", EinoSnapshot: snapshot}
	original, _ := json.Marshal(envelope)
	rawStore := &memoryStore{data: map[string][]byte{"checkpoint": original}}
	input := types.Input{Message: schema.UserMessage("pending"), Meta: map[string]string{"MessageID": "9007199254740993"}}
	appendInputsErr := AppendInputs(context.Background(), rawStore, "checkpoint", "thread", "run", []types.Input{input})
	if appendInputsErr != nil {
		t.Fatal(appendInputsErr)
	}
	var saved Envelope
	savedDecodeErr := json.Unmarshal(rawStore.data["checkpoint"], &saved)
	if savedDecodeErr != nil {
		t.Fatal(savedDecodeErr)
	}
	var root, fields, value map[string]json.RawMessage
	rootDecodeErr := json.Unmarshal(saved.EinoSnapshot, &root)
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
