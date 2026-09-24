package checkpointer

import (
	"context"
	"encoding/json"
	"fmt"

	"eino-cli/deepagent/core/types"
)

// FenceTool marks a call in an existing Eino checkpoint before invoking it.
// A crash leaves an unknown outcome and requires reconciliation before resume.
// It does not synthesize an Eino execution cursor when no checkpoint exists.
// The caller serializes concurrent calls on the same checkpoint.
func (s *Store) FenceTool(ctx context.Context, id string, call types.ToolCall, required bool) error {
	snapshot, exists, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		if required {
			return fmt.Errorf("restored checkpoint %q disappeared before tool execution", id)
		}
		return nil
	}
	var root, fields, value map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &root); err != nil {
		return err
	}
	if err := json.Unmarshal(root["MapValues"], &fields); err != nil {
		return err
	}
	if err := json.Unmarshal(fields["State"], &value); err != nil {
		return err
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(value["JSONValue"], &state); err != nil {
		return err
	}
	var calls []types.ToolCallState
	if raw := state["Calls"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &calls); err != nil {
			return err
		}
	}
	found := false
	for i := range calls {
		if calls[i].Call.ID == call.ID {
			calls[i].Status = types.CallOutcomeUnknown
			calls[i].Result = nil
			found = true
			break
		}
	}
	if !found {
		calls = append(calls, types.ToolCallState{Call: call, Status: types.CallOutcomeUnknown})
	}
	state["Calls"], err = json.Marshal(calls)
	if err != nil {
		return err
	}
	value["JSONValue"], err = json.Marshal(state)
	if err != nil {
		return err
	}
	fields["State"], err = json.Marshal(value)
	if err != nil {
		return err
	}
	root["MapValues"], err = json.Marshal(fields)
	if err != nil {
		return err
	}
	snapshot, err = json.Marshal(root)
	if err != nil {
		return err
	}
	return s.Set(ctx, id, snapshot)
}

func rejectUnknownToolOutcome(snapshot []byte) error {
	var root struct{ MapValues map[string]json.RawMessage }
	if err := json.Unmarshal(snapshot, &root); err != nil {
		return err
	}
	var state struct {
		JSONValue struct{ Calls []types.ToolCallState }
	}
	if err := json.Unmarshal(root.MapValues["State"], &state); err != nil {
		return err
	}
	for _, call := range state.JSONValue.Calls {
		if call.Status == types.CallOutcomeUnknown || call.Status == types.CallRunning {
			return fmt.Errorf("tool call %s has unknown outcome; explicit reconciliation required", call.Call.ID)
		}
	}
	return nil
}
