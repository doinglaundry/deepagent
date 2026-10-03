package checkpointer

import (
	"encoding/json"
	"fmt"

	"eino-cli/deepagent/graph/types"
)

// ToolOutcomeUnknownError stops replay while preserving the original inputs
// for the failed Run's terminal event and delayed message redelivery.
type ToolOutcomeUnknownError struct {
	CallID string
	Inputs []types.Input
}

func (e *ToolOutcomeUnknownError) Error() string {
	return fmt.Sprintf("tool call %s has unknown outcome; explicit reconciliation required", e.CallID)
}

// snapshotState edits only Eino's canonical JSON local state. The surrounding
// maps retain execution inputs, interrupt addresses and unknown wire fields.
type snapshotState struct {
	root, fields, wrapper, value map[string]json.RawMessage
}

func openSnapshot(snapshot []byte) (*snapshotState, error) {
	state := &snapshotState{}
	err := json.Unmarshal(snapshot, &state.root)
	if err != nil {
		return nil, fmt.Errorf("decode Eino checkpoint: %w", err)
	}
	err = json.Unmarshal(state.root["MapValues"], &state.fields)
	if err != nil {
		return nil, nil
	}
	err = json.Unmarshal(state.fields["State"], &state.wrapper)
	if err != nil {
		return nil, nil
	}
	var typ struct{ SimpleType string }
	err = json.Unmarshal(state.wrapper["Type"], &typ)
	if err != nil || typ.SimpleType != "deepagent_run_state_v1" {
		return nil, nil
	}
	err = json.Unmarshal(state.wrapper["JSONValue"], &state.value)
	if err != nil || state.value == nil {
		return nil, fmt.Errorf("invalid checkpoint RunState: %v", err)
	}
	return state, nil
}

func (s *snapshotState) encode() ([]byte, error) {
	var err error
	s.wrapper["JSONValue"], err = json.Marshal(s.value)
	if err != nil {
		return nil, err
	}
	s.fields["State"], err = json.Marshal(s.wrapper)
	if err != nil {
		return nil, err
	}
	s.root["MapValues"], err = json.Marshal(s.fields)
	if err != nil {
		return nil, err
	}
	return json.Marshal(s.root)
}

func (s *snapshotState) runState() (*types.RunState, error) {
	raw, err := json.Marshal(s.value)
	if err != nil {
		return nil, err
	}
	var state types.RunState
	err = json.Unmarshal(raw, &state)
	if err != nil {
		return nil, err
	}
	if state.Version != 1 {
		return nil, fmt.Errorf("unsupported checkpoint RunState version %d", state.Version)
	}
	if state.PreparedInputs < 0 || state.PreparedInputs > len(state.Consumed) {
		return nil, fmt.Errorf("invalid prepared input cursor %d", state.PreparedInputs)
	}
	return &state, nil
}

func requiredSnapshot(snapshot []byte) (*snapshotState, error) {
	state, err := openSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return nil, fmt.Errorf("checkpoint requires canonical RunState")
	}
	return state, nil
}

func blockedSnapshot(snapshot []byte) ([]byte, error) {
	state, err := openSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	if state == nil {
		return snapshot, nil
	}
	state.value["Phase"] = json.RawMessage(`"blocked"`)
	return state.encode()
}

func rejectTerminalSnapshot(snapshot []byte) error {
	state, err := openSnapshot(snapshot)
	if err != nil || state == nil {
		return err
	}
	value, err := state.runState()
	if err != nil {
		return err
	}
	switch value.Phase {
	case types.PhaseCompleted, types.PhaseFailed, types.PhaseInterrupted:
		return fmt.Errorf("checkpoint run is terminal: %s", value.Phase)
	}
	return nil
}

func rejectUnknownToolOutcome(snapshot []byte) error {
	state, err := openSnapshot(snapshot)
	if err != nil || state == nil {
		return err
	}
	value, err := state.runState()
	if err != nil {
		return err
	}
	for _, call := range value.Calls {
		if call.Status == types.CallOutcomeUnknown || call.Status == types.CallRunning {
			return &ToolOutcomeUnknownError{CallID: call.Call.ID, Inputs: value.Consumed}
		}
	}
	return nil
}

// mergeObject retains unrecognized nested call fields when terminal state
// replaces known values. Arrays and scalars remain authoritative replacements.
func mergeObject(old, current json.RawMessage, fieldName string) json.RawMessage {
	var previous, next map[string]json.RawMessage
	oldErr := json.Unmarshal(old, &previous)
	nextErr := json.Unmarshal(current, &next)
	if oldErr != nil || nextErr != nil || previous == nil || next == nil {
		return current
	}
	if fieldName == "Result" {
		_, includesContent := next["MultiContent"]
		if !includesContent {
			delete(previous, "MultiContent")
		}
	}
	for key, value := range next {
		previous[key] = mergeObject(previous[key], value, key)
	}
	merged, _ := json.Marshal(previous)
	return merged
}

func preserveCallFields(old, current json.RawMessage) json.RawMessage {
	var previous, next []json.RawMessage
	oldErr := json.Unmarshal(old, &previous)
	nextErr := json.Unmarshal(current, &next)
	if oldErr != nil || nextErr != nil {
		return current
	}
	byID := map[string]json.RawMessage{}
	for _, raw := range previous {
		var item types.ToolCallState
		err := json.Unmarshal(raw, &item)
		if err == nil {
			byID[item.Call.ID] = raw
		}
	}
	for i, raw := range next {
		var item types.ToolCallState
		err := json.Unmarshal(raw, &item)
		if err == nil {
			next[i] = mergeObject(byID[item.Call.ID], raw, "")
		}
	}
	merged, _ := json.Marshal(next)
	return merged
}
