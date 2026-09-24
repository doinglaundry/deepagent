package checkpointer

import (
	"context"
	"eino-cli/deepagent/core/types"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/compose"
)

// AppendInputs seals the Thread's accepted-input boundary in the same Eino
// snapshot before a blocked event can release ownership. The caller must have
// stopped accepting inputs and still own the checkpoint's storage permit.
func AppendInputs(ctx context.Context, store compose.CheckPointStore, id, threadID, runID string, inputs []types.Input) error {
	if len(inputs) == 0 {
		return nil
	}
	if store == nil {
		return fmt.Errorf("pending inputs require checkpoint store")
	}
	raw, exists, err := store.Get(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("pending input checkpoint not found")
	}
	var envelope Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	if envelope.Version != 1 || envelope.GraphVersion != "core-graph-v1" || envelope.ThreadID != threadID || envelope.RunID != runID {
		return fmt.Errorf("pending input checkpoint identity mismatch")
	}
	var root, fields, value map[string]json.RawMessage
	if err = json.Unmarshal(envelope.EinoSnapshot, &root); err != nil {
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
		return fmt.Errorf("pending input checkpoint state type mismatch")
	}
	var state types.RunState
	if err = json.Unmarshal(value["JSONValue"], &state); err != nil {
		return err
	}
	if state.Version != 1 || state.ThreadID != threadID || state.RunID != runID || state.Phase != types.PhaseBlocked {
		return fmt.Errorf("pending input checkpoint is not a matching blocked run")
	}
	for _, input := range inputs {
		if input.Message == nil {
			return fmt.Errorf("pending checkpoint input has no message")
		}
	}
	state.Consumed = append(state.Consumed, inputs...)
	if state.Extensions == nil {
		state.Extensions = map[string]json.RawMessage{}
	}
	state.Extensions["pending_inputs"] = json.RawMessage(`true`)
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
	envelope.EinoSnapshot, err = json.Marshal(root)
	if err != nil {
		return err
	}
	raw, err = json.Marshal(envelope)
	if err != nil {
		return err
	}
	return store.Set(ctx, id, raw)
}
