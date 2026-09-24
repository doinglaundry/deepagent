package checkpointer

import (
	"context"
	"encoding/json"
	"fmt"

	"eino-cli/deepagent/core/types"
)

// Finalize replaces the local state in the existing Eino snapshot. It does not
// create a second completion record or erase the checkpoint's provenance.
// The caller still owns the Thread's storage permit until this write finishes.
func (s *Store) Finalize(ctx context.Context, id string, state *types.RunState, required bool) error {
	if state == nil || (state.Phase != types.PhaseCompleted && state.Phase != types.PhaseFailed && state.Phase != types.PhaseInterrupted) || state.ThreadID != s.threadID || state.RunID != s.runID {
		return fmt.Errorf("checkpoint terminal state mismatch")
	}
	snapshot, exists, err := s.get(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		if required {
			return fmt.Errorf("restored checkpoint %q disappeared before terminal write", id)
		}
		return nil
	}
	var root, fields, value map[string]json.RawMessage
	if err = json.Unmarshal(snapshot, &root); err != nil {
		return err
	}
	if err = json.Unmarshal(root["MapValues"], &fields); err != nil {
		return err
	}
	if err = json.Unmarshal(fields["State"], &value); err != nil {
		return err
	}
	var typ struct{ SimpleType string }
	if err = json.Unmarshal(value["Type"], &typ); err != nil || typ.SimpleType != "deepagent_run_state_v1" {
		return fmt.Errorf("checkpoint completion requires canonical RunState")
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
	raw, err := json.Marshal(Envelope{Version: 1, ThreadID: s.threadID, RunID: s.runID, GraphVersion: s.graphVersion, EinoSnapshot: snapshot})
	if err != nil {
		return err
	}
	// Set normalizes an Eino interrupt to blocked; terminal snapshots must
	// bypass that normalization and go to the same underlying key directly.
	return s.inner.Set(ctx, id, raw)
}

func rejectTerminalSnapshot(snapshot []byte) error {
	var root struct{ MapValues map[string]json.RawMessage }
	if err := json.Unmarshal(snapshot, &root); err != nil {
		return err
	}
	var state struct {
		Type      struct{ SimpleType string }
		JSONValue struct{ Phase types.Phase }
	}
	if err := json.Unmarshal(root.MapValues["State"], &state); err != nil {
		return err
	}
	if state.Type.SimpleType != "deepagent_run_state_v1" {
		return nil
	}
	switch state.JSONValue.Phase {
	case types.PhaseCompleted, types.PhaseFailed, types.PhaseInterrupted:
		return fmt.Errorf("checkpoint run is terminal: %s", state.JSONValue.Phase)
	}
	return nil
}
