// Package checkpointer stores Eino execution snapshots with their Run identity.
package checkpointer

import (
	"context"
	"encoding/json"
	"fmt"

	"eino-cli/deepagent/graph/types"

	"github.com/cloudwego/eino/compose"
)

// GraphStore 校验快照归属，将底层字节存储接入 Eino。
type GraphStore struct {
	storage  compose.CheckPointStore
	threadID string
	runID    string
}

var _ compose.CheckPointStore = (*GraphStore)(nil)

func NewGraphStore(storage compose.CheckPointStore, threadID, runID string) *GraphStore {
	return &GraphStore{storage: storage, threadID: threadID, runID: runID}
}

func (graphStore *GraphStore) Get(ctx context.Context, checkpointID string) ([]byte, bool, error) {
	snapshot, exists, err := graphStore.storage.Get(ctx, checkpointID)
	if err != nil || !exists {
		return snapshot, exists, err
	}
	_, runState, err := decodeActiveSnapshot(snapshot, graphStore.threadID, graphStore.runID)
	if err != nil {
		return nil, false, err
	}
	err = rejectUnknownToolOutcome(runState)
	if err != nil {
		return nil, false, err
	}
	return snapshot, true, nil
}

// Set 只保存 Eino 快照；身份和版本均来自其中的 RunState。
func (graphStore *GraphStore) Set(ctx context.Context, checkpointID string, snapshot []byte) error {
	snapshotState, _, err := decodeActiveSnapshot(snapshot, graphStore.threadID, graphStore.runID)
	if err != nil {
		return err
	}
	snapshotState.runStateFields["Phase"] = json.RawMessage(`"blocked"`)
	snapshot, err = snapshotState.encodeSnapshot()
	if err != nil {
		return err
	}
	return graphStore.storage.Set(ctx, checkpointID, snapshot)
}

// SaveInterrupts 保存 Eino 分配的中断 ID，供后续回答或审批定位暂停点。
func (graphStore *GraphStore) SaveInterrupts(ctx context.Context, checkpointID string, pending []types.Interrupt) error {
	snapshot, exists, err := graphStore.storage.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("interrupted checkpoint not found")
	}
	interruptIDs := make([]string, 0, len(pending))
	for _, interrupt := range pending {
		if interrupt.CheckpointID != checkpointID {
			return fmt.Errorf("pending checkpoint identity mismatch")
		}
		interruptIDs = append(interruptIDs, interrupt.InterruptID)
	}
	err = ValidateResume(snapshot, interruptIDs, nil)
	if err != nil {
		return err
	}
	snapshotState, runState, err := decodeActiveSnapshot(snapshot, graphStore.threadID, graphStore.runID)
	if err != nil {
		return err
	}
	err = rejectUnknownToolOutcome(runState)
	if err != nil {
		return err
	}
	snapshotState.runStateFields["Phase"] = json.RawMessage(`"blocked"`)
	snapshotState.runStateFields["Pending"], err = json.Marshal(pending)
	if err != nil {
		return err
	}
	snapshot, err = snapshotState.encodeSnapshot()
	if err != nil {
		return err
	}
	return graphStore.storage.Set(ctx, checkpointID, snapshot)
}

// AppendInputs seals accepted inputs before Thread publishes its blocked event.
// PreparedInputs stays unchanged: newly appended inputs still need persistence.
func AppendInputs(ctx context.Context, storage compose.CheckPointStore, checkpointID, threadID, runID string, inputs []types.Input) error {
	if len(inputs) == 0 {
		return nil
	}
	if storage == nil {
		return fmt.Errorf("pending inputs require checkpoint store")
	}
	snapshot, exists, err := storage.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("pending input checkpoint not found")
	}
	snapshotState, runState, err := decodeActiveSnapshot(snapshot, threadID, runID)
	if err != nil {
		return err
	}
	if runState.Phase != types.PhaseBlocked {
		return fmt.Errorf("pending input checkpoint is not a matching blocked run")
	}
	for _, input := range inputs {
		if input.Message == nil {
			return fmt.Errorf("pending checkpoint input has no message")
		}
	}
	runState.Consumed = append(runState.Consumed, inputs...)
	snapshotState.runStateFields["Consumed"], err = json.Marshal(runState.Consumed)
	if err != nil {
		return err
	}
	snapshot, err = snapshotState.encodeSnapshot()
	if err != nil {
		return err
	}
	return storage.Set(ctx, checkpointID, snapshot)
}

// MarkToolOutcomeUnknown 在副作用执行前保存结果未知的标记，防止崩溃后重复执行。
// 调用方串行保存这些标记；同一快照可以包含多个结果未知的调用。
func (graphStore *GraphStore) MarkToolOutcomeUnknown(ctx context.Context, checkpointID string, call types.ToolCall, mustExist bool) error {
	snapshot, exists, err := graphStore.storage.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if !exists {
		if mustExist {
			return fmt.Errorf("restored checkpoint %q disappeared before tool execution", checkpointID)
		}
		return nil
	}
	snapshotState, _, err := decodeActiveSnapshot(snapshot, graphStore.threadID, graphStore.runID)
	if err != nil {
		return err
	}
	var calls []map[string]json.RawMessage
	raw := snapshotState.runStateFields["Calls"]
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
	snapshotState.runStateFields["Phase"] = json.RawMessage(`"blocked"`)
	snapshotState.runStateFields["Calls"], err = json.Marshal(calls)
	if err != nil {
		return err
	}
	snapshot, err = snapshotState.encodeSnapshot()
	if err != nil {
		return err
	}
	return graphStore.storage.Set(ctx, checkpointID, snapshot)
}

// SaveTerminalState 保存最终状态，保留 Eino 执行位置和未知字段。
func (graphStore *GraphStore) SaveTerminalState(ctx context.Context, checkpointID string, runState *types.RunState, mustExist bool) error {
	if runState == nil || runState.ThreadID != graphStore.threadID || runState.RunID != graphStore.runID {
		return fmt.Errorf("checkpoint terminal state mismatch")
	}
	switch runState.Phase {
	case types.PhaseCompleted, types.PhaseFailed, types.PhaseInterrupted:
	default:
		return fmt.Errorf("checkpoint terminal state mismatch")
	}
	snapshot, exists, err := graphStore.storage.Get(ctx, checkpointID)
	if err != nil {
		return err
	}
	if !exists {
		if mustExist {
			return fmt.Errorf("restored checkpoint %q disappeared before terminal write", checkpointID)
		}
		return nil
	}
	snapshotState, _, err := decodeActiveSnapshot(snapshot, graphStore.threadID, graphStore.runID)
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
	current["Calls"] = preserveCallFields(snapshotState.runStateFields["Calls"], current["Calls"])
	for key, field := range current {
		snapshotState.runStateFields[key] = field
	}
	snapshot, err = snapshotState.encodeSnapshot()
	if err != nil {
		return err
	}
	return graphStore.storage.Set(ctx, checkpointID, snapshot)
}
