package checkpointer

import (
	"context"
	"encoding/json"
	"fmt"

	"eino-cli/deepagent/graph/types"
	"github.com/cloudwego/eino/compose"
)

// SavePending records Eino-assigned interrupt IDs in its existing local state.
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
	err = ValidateResume(snapshot, ids, nil)
	if err != nil {
		return err
	}
	state, err := requiredSnapshot(snapshot)
	if err != nil {
		return err
	}
	state.value["Pending"], err = json.Marshal(pending)
	if err != nil {
		return err
	}
	snapshot, err = state.encode()
	if err != nil {
		return err
	}
	return s.Set(ctx, id, snapshot)
}

// AppendInputs seals accepted inputs before Thread publishes its blocked event.
// PreparedInputs stays unchanged: newly appended inputs still need persistence.
func AppendInputs(ctx context.Context, store compose.CheckPointStore, id, threadID, runID string, inputs []types.Input) error {
	if len(inputs) == 0 {
		return nil
	}
	if store == nil {
		return fmt.Errorf("pending inputs require checkpoint store")
	}
	wrapper := New(store, threadID, runID, "core-graph-v1")
	snapshot, exists, err := wrapper.get(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("pending input checkpoint not found")
	}
	state, err := requiredSnapshot(snapshot)
	if err != nil {
		return err
	}
	value, err := state.runState()
	if err != nil {
		return err
	}
	if value.ThreadID != threadID || value.RunID != runID || value.Phase != types.PhaseBlocked {
		return fmt.Errorf("pending input checkpoint is not a matching blocked run")
	}
	for _, input := range inputs {
		if input.Message == nil {
			return fmt.Errorf("pending checkpoint input has no message")
		}
	}
	value.Consumed = append(value.Consumed, inputs...)
	state.value["Consumed"], err = json.Marshal(value.Consumed)
	if err != nil {
		return err
	}
	snapshot, err = state.encode()
	if err != nil {
		return err
	}
	return wrapper.Set(ctx, id, snapshot)
}

// FenceTool writes unknown outcome before invoking a side effect. The caller
// serializes fences; get intentionally allows another already-fenced call.
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
	state, err := requiredSnapshot(snapshot)
	if err != nil {
		return err
	}
	var calls []map[string]json.RawMessage
	raw := state.value["Calls"]
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &calls)
		if err != nil {
			return err
		}
	}
	var target map[string]json.RawMessage
	for _, item := range calls {
		var identity types.ToolCall
		err = json.Unmarshal(item["Call"], &identity)
		if err != nil {
			return err
		}
		if identity.ID == call.ID {
			target = item
			break
		}
	}
	if target == nil {
		encoded, encodeErr := json.Marshal(call)
		if encodeErr != nil {
			return encodeErr
		}
		target = map[string]json.RawMessage{"Call": encoded}
		calls = append(calls, target)
	}
	target["Status"] = json.RawMessage(`"outcome_unknown"`)
	target["Result"] = json.RawMessage(`null`)
	state.value["Calls"], err = json.Marshal(calls)
	if err != nil {
		return err
	}
	snapshot, err = state.encode()
	if err != nil {
		return err
	}
	return s.Set(ctx, id, snapshot)
}

// Finalize replaces known local-state fields in the same snapshot without
// erasing Eino's execution provenance or turning a terminal state into blocked.
func (s *Store) Finalize(ctx context.Context, id string, value *types.RunState, required bool) error {
	if value == nil || (value.Phase != types.PhaseCompleted && value.Phase != types.PhaseFailed && value.Phase != types.PhaseInterrupted) || value.ThreadID != s.threadID || value.RunID != s.runID {
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
	state, err := requiredSnapshot(snapshot)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var current map[string]json.RawMessage
	err = json.Unmarshal(raw, &current)
	if err != nil {
		return err
	}
	current["Calls"] = preserveCallFields(state.value["Calls"], current["Calls"])
	for key, field := range current {
		state.value[key] = field
	}
	snapshot, err = state.encode()
	if err != nil {
		return err
	}
	return s.writeSnapshot(ctx, id, snapshot)
}
