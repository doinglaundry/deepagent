package checkpointer

import (
	"context"
	"encoding/json"
	"fmt"

	"eino-cli/deepagent/graph/types"
	"github.com/cloudwego/eino/compose"
)

// SavePending records Eino-assigned interrupt IDs in its existing local state.
func (checkpointStore *Store) SavePending(ctx context.Context, id string, pending []types.Interrupt) error {
	snapshot, exists, err := checkpointStore.Get(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("interrupted checkpoint not found")
	}
	interruptIDs := make([]string, 0, len(pending))
	for _, interrupt := range pending {
		if interrupt.CheckpointID != id {
			return fmt.Errorf("pending checkpoint identity mismatch")
		}
		interruptIDs = append(interruptIDs, interrupt.InterruptID)
	}
	err = ValidateResume(snapshot, interruptIDs, nil)
	if err != nil {
		return err
	}
	snapshotState, err := requireSnapshotState(snapshot)
	if err != nil {
		return err
	}
	snapshotState.value["Pending"], err = json.Marshal(pending)
	if err != nil {
		return err
	}
	snapshot, err = snapshotState.encodeSnapshot()
	if err != nil {
		return err
	}
	return checkpointStore.Set(ctx, id, snapshot)
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
	checkpointStore := New(store, threadID, runID, "core-graph-v1")
	snapshot, exists, err := checkpointStore.readSnapshot(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("pending input checkpoint not found")
	}
	snapshotState, err := requireSnapshotState(snapshot)
	if err != nil {
		return err
	}
	runState, err := snapshotState.decodeRunState()
	if err != nil {
		return err
	}
	if runState.ThreadID != threadID || runState.RunID != runID || runState.Phase != types.PhaseBlocked {
		return fmt.Errorf("pending input checkpoint is not a matching blocked run")
	}
	for _, input := range inputs {
		if input.Message == nil {
			return fmt.Errorf("pending checkpoint input has no message")
		}
	}
	runState.Consumed = append(runState.Consumed, inputs...)
	snapshotState.value["Consumed"], err = json.Marshal(runState.Consumed)
	if err != nil {
		return err
	}
	snapshot, err = snapshotState.encodeSnapshot()
	if err != nil {
		return err
	}
	return checkpointStore.Set(ctx, id, snapshot)
}

// FenceTool writes unknown outcome before invoking a side effect. The caller
// serializes fences; get intentionally allows another already-fenced call.
func (checkpointStore *Store) FenceTool(ctx context.Context, id string, call types.ToolCall, required bool) error {
	snapshot, exists, err := checkpointStore.readSnapshot(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		if required {
			return fmt.Errorf("restored checkpoint %q disappeared before tool execution", id)
		}
		return nil
	}
	snapshotState, err := requireSnapshotState(snapshot)
	if err != nil {
		return err
	}
	var calls []map[string]json.RawMessage
	raw := snapshotState.value["Calls"]
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &calls)
		if err != nil {
			return err
		}
	}
	var targetCall map[string]json.RawMessage
	for _, callFields := range calls {
		var toolCall types.ToolCall
		err = json.Unmarshal(callFields["Call"], &toolCall)
		if err != nil {
			return err
		}
		if toolCall.ID == call.ID {
			targetCall = callFields
			break
		}
	}
	if targetCall == nil {
		encoded, encodeErr := json.Marshal(call)
		if encodeErr != nil {
			return encodeErr
		}
		targetCall = map[string]json.RawMessage{"Call": encoded}
		calls = append(calls, targetCall)
	}
	targetCall["Status"] = json.RawMessage(`"outcome_unknown"`)
	targetCall["Result"] = json.RawMessage(`null`)
	snapshotState.value["Calls"], err = json.Marshal(calls)
	if err != nil {
		return err
	}
	snapshot, err = snapshotState.encodeSnapshot()
	if err != nil {
		return err
	}
	return checkpointStore.Set(ctx, id, snapshot)
}

// Finalize replaces known local-state fields in the same snapshot without
// erasing Eino's execution provenance or turning a terminal state into blocked.
func (checkpointStore *Store) Finalize(ctx context.Context, id string, runState *types.RunState, required bool) error {
	if runState == nil || (runState.Phase != types.PhaseCompleted && runState.Phase != types.PhaseFailed && runState.Phase != types.PhaseInterrupted) || runState.ThreadID != checkpointStore.threadID || runState.RunID != checkpointStore.runID {
		return fmt.Errorf("checkpoint terminal state mismatch")
	}
	snapshot, exists, err := checkpointStore.readSnapshot(ctx, id)
	if err != nil {
		return err
	}
	if !exists {
		if required {
			return fmt.Errorf("restored checkpoint %q disappeared before terminal write", id)
		}
		return nil
	}
	snapshotState, err := requireSnapshotState(snapshot)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(runState)
	if err != nil {
		return err
	}
	var current map[string]json.RawMessage
	err = json.Unmarshal(raw, &current)
	if err != nil {
		return err
	}
	current["Calls"] = preserveCallFields(snapshotState.value["Calls"], current["Calls"])
	for key, field := range current {
		snapshotState.value[key] = field
	}
	snapshot, err = snapshotState.encodeSnapshot()
	if err != nil {
		return err
	}
	return checkpointStore.writeSnapshot(ctx, id, snapshot)
}
