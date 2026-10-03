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

func (outcomeError *ToolOutcomeUnknownError) Error() string {
	return fmt.Sprintf("tool call %s has unknown outcome; explicit reconciliation required", outcomeError.CallID)
}

// snapshotState edits only Eino's canonical JSON local state. The surrounding
// maps retain execution inputs, interrupt addresses and unknown wire fields.
type snapshotState struct {
	root, fields, wrapper, value map[string]json.RawMessage
}

func decodeSnapshot(snapshot []byte) (*snapshotState, error) {
	snapshotState := &snapshotState{}
	err := json.Unmarshal(snapshot, &snapshotState.root)
	if err != nil {
		return nil, fmt.Errorf("decode Eino checkpoint: %w", err)
	}
	err = json.Unmarshal(snapshotState.root["MapValues"], &snapshotState.fields)
	if err != nil {
		return nil, nil
	}
	err = json.Unmarshal(snapshotState.fields["State"], &snapshotState.wrapper)
	if err != nil {
		return nil, nil
	}
	var stateType struct{ SimpleType string }
	err = json.Unmarshal(snapshotState.wrapper["Type"], &stateType)
	if err != nil || stateType.SimpleType != "deepagent_run_state_v1" {
		return nil, nil
	}
	err = json.Unmarshal(snapshotState.wrapper["JSONValue"], &snapshotState.value)
	if err != nil || snapshotState.value == nil {
		return nil, fmt.Errorf("invalid checkpoint RunState: %v", err)
	}
	return snapshotState, nil
}

func (snapshotState *snapshotState) encodeSnapshot() ([]byte, error) {
	var err error
	snapshotState.wrapper["JSONValue"], err = json.Marshal(snapshotState.value)
	if err != nil {
		return nil, err
	}
	snapshotState.fields["State"], err = json.Marshal(snapshotState.wrapper)
	if err != nil {
		return nil, err
	}
	snapshotState.root["MapValues"], err = json.Marshal(snapshotState.fields)
	if err != nil {
		return nil, err
	}
	return json.Marshal(snapshotState.root)
}

func (snapshotState *snapshotState) decodeRunState() (*types.RunState, error) {
	raw, err := json.Marshal(snapshotState.value)
	if err != nil {
		return nil, err
	}
	var runState types.RunState
	err = json.Unmarshal(raw, &runState)
	if err != nil {
		return nil, err
	}
	if runState.Version != 1 {
		return nil, fmt.Errorf("unsupported checkpoint RunState version %d", runState.Version)
	}
	if runState.PreparedInputs < 0 || runState.PreparedInputs > len(runState.Consumed) {
		return nil, fmt.Errorf("invalid prepared input cursor %d", runState.PreparedInputs)
	}
	return &runState, nil
}

func requireSnapshotState(snapshot []byte) (*snapshotState, error) {
	snapshotState, err := decodeSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	if snapshotState == nil {
		return nil, fmt.Errorf("checkpoint requires canonical RunState")
	}
	return snapshotState, nil
}

func markSnapshotBlocked(snapshot []byte) ([]byte, error) {
	snapshotState, err := decodeSnapshot(snapshot)
	if err != nil {
		return nil, err
	}
	if snapshotState == nil {
		return snapshot, nil
	}
	snapshotState.value["Phase"] = json.RawMessage(`"blocked"`)
	return snapshotState.encodeSnapshot()
}

func rejectTerminalSnapshot(snapshot []byte) error {
	snapshotState, err := decodeSnapshot(snapshot)
	if err != nil || snapshotState == nil {
		return err
	}
	runState, err := snapshotState.decodeRunState()
	if err != nil {
		return err
	}
	switch runState.Phase {
	case types.PhaseCompleted, types.PhaseFailed, types.PhaseInterrupted:
		return fmt.Errorf("checkpoint run is terminal: %s", runState.Phase)
	}
	return nil
}

func rejectUnknownToolOutcome(snapshot []byte) error {
	snapshotState, err := decodeSnapshot(snapshot)
	if err != nil || snapshotState == nil {
		return err
	}
	runState, err := snapshotState.decodeRunState()
	if err != nil {
		return err
	}
	for _, call := range runState.Calls {
		if call.Status == types.CallOutcomeUnknown || call.Status == types.CallRunning {
			return &ToolOutcomeUnknownError{CallID: call.Call.ID, Inputs: runState.Consumed}
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
	callsByID := map[string]json.RawMessage{}
	for _, raw := range previous {
		var toolCallState types.ToolCallState
		err := json.Unmarshal(raw, &toolCallState)
		if err == nil {
			callsByID[toolCallState.Call.ID] = raw
		}
	}
	for i, raw := range next {
		var toolCallState types.ToolCallState
		err := json.Unmarshal(raw, &toolCallState)
		if err == nil {
			next[i] = mergeObject(callsByID[toolCallState.Call.ID], raw, "")
		}
	}
	merged, _ := json.Marshal(next)
	return merged
}
