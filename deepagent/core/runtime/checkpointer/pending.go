package checkpointer

import (
	"context"
	"eino-cli/deepagent/core/types"
	"encoding/json"
	"fmt"
)

// SavePending enriches the existing Eino local state after Eino assigns final
// interrupt IDs. It updates the same snapshot; no sidecar state is introduced.
func (s *Store) SavePending(ctx context.Context, id string, pending []types.Interrupt) error {
	snapshot, exists, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("interrupted checkpoint not found")
	}
	ids := make([]string, 0, len(pending))
	for _, item := range pending {
		if item.CheckpointID != id {
			return fmt.Errorf("pending checkpoint identity mismatch")
		}
		ids = append(ids, item.InterruptID)
	}
	if err := ValidateResume(snapshot, ids, nil); err != nil {
		return err
	}
	var root, fields, value, state map[string]json.RawMessage
	if err := json.Unmarshal(snapshot, &root); err != nil {
		return err
	}
	if err := json.Unmarshal(root["MapValues"], &fields); err != nil {
		return err
	}
	if err := json.Unmarshal(fields["State"], &value); err != nil {
		return err
	}
	if err := json.Unmarshal(value["JSONValue"], &state); err != nil {
		return err
	}
	if state == nil {
		return fmt.Errorf("missing canonical checkpoint state")
	}
	state["Pending"], err = json.Marshal(pending)
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
