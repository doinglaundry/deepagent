package checkpointer

import (
	"encoding/json"
	"fmt"
)

// Eino v0.9.0-alpha.17 saves the local state before returning its external
// before/after-node interrupt. Those pauses do not execute our node error
// handler. Normalize the canonical state's phase inside that same snapshot.
// Keep all other wire fields verbatim, including caller-owned input metadata.
func blockedSnapshot(snapshot []byte) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &root); err != nil {
		return nil, fmt.Errorf("decode Eino checkpoint: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(root["MapValues"], &fields); err != nil {
		return snapshot, nil
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(fields["State"], &state); err != nil {
		return snapshot, nil
	}
	var typ struct{ SimpleType string }
	if err := json.Unmarshal(state["Type"], &typ); err != nil || typ.SimpleType != "deepagent_run_state_v1" {
		return snapshot, nil
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(state["JSONValue"], &value); err != nil {
		return nil, fmt.Errorf("decode checkpoint RunState: %w", err)
	}
	value["Phase"] = json.RawMessage(`"blocked"`)
	var err error
	state["JSONValue"], err = json.Marshal(value)
	if err != nil {
		return nil, err
	}
	fields["State"], err = json.Marshal(state)
	if err != nil {
		return nil, err
	}
	root["MapValues"], err = json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return json.Marshal(root)
}
