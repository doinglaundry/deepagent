package checkpointer

import (
	"encoding/json"
	"fmt"
	"strconv"

	"eino-cli/deepagent/graph/types"
)

// snapshotState edits only Eino's canonical JSON local state. The surrounding
// maps retain execution inputs, interrupt addresses and unknown wire fields.
type snapshotState struct {
	snapshotFields   map[string]json.RawMessage
	graphFields      map[string]json.RawMessage
	localStateFields map[string]json.RawMessage
	runStateFields   map[string]json.RawMessage
}

// interruptTree 只读取 Eino 的中断地址及嵌套子图。
type interruptTree struct {
	MapValues map[string]*interruptTree `json:",omitempty"`
}

// ToolOutcomeUnknownError stops replay while preserving the original inputs
// for the failed Run's terminal event and delayed message redelivery.
type ToolOutcomeUnknownError struct {
	CallID string
	Inputs []types.Input
}

func (outcomeError *ToolOutcomeUnknownError) Error() string {
	return fmt.Sprintf("tool call %s has unknown outcome; explicit reconciliation required", outcomeError.CallID)
}

func decodeSnapshot(snapshot []byte) (*snapshotState, error) {
	snapshotState := &snapshotState{}
	err := json.Unmarshal(snapshot, &snapshotState.snapshotFields)
	if err != nil {
		return nil, fmt.Errorf("decode Eino checkpoint: %w", err)
	}
	err = json.Unmarshal(snapshotState.snapshotFields["MapValues"], &snapshotState.graphFields)
	if err != nil {
		return nil, fmt.Errorf("checkpoint requires canonical RunState")
	}
	err = json.Unmarshal(snapshotState.graphFields["State"], &snapshotState.localStateFields)
	if err != nil {
		return nil, fmt.Errorf("checkpoint requires canonical RunState")
	}
	var stateType struct{ SimpleType string }
	err = json.Unmarshal(snapshotState.localStateFields["Type"], &stateType)
	if err != nil || stateType.SimpleType != "deepagent_run_state_v1" {
		return nil, fmt.Errorf("checkpoint requires canonical RunState")
	}
	err = json.Unmarshal(snapshotState.localStateFields["JSONValue"], &snapshotState.runStateFields)
	if err != nil || snapshotState.runStateFields == nil {
		return nil, fmt.Errorf("invalid checkpoint RunState: %v", err)
	}
	return snapshotState, nil
}

// 业务更新需要本项目的 RunState，同时保留 Eino 的执行位置和未知字段。
func decodeActiveSnapshot(snapshot []byte, threadID, runID string) (*snapshotState, *types.RunState, error) {
	snapshotState, err := decodeSnapshot(snapshot)
	if err != nil {
		return nil, nil, err
	}
	runState, err := snapshotState.decodeActiveRunState()
	if err != nil {
		return nil, nil, err
	}
	if runState.ThreadID != threadID || runState.RunID != runID || runState.RunID == "" {
		return nil, nil, fmt.Errorf("checkpoint identity mismatch")
	}
	return snapshotState, runState, nil
}

// 直接解析 Eino 保存的 RunState，校验输入游标和终止状态。
func (snapshotState *snapshotState) decodeActiveRunState() (*types.RunState, error) {
	var runState types.RunState
	err := json.Unmarshal(snapshotState.localStateFields["JSONValue"], &runState)
	if err != nil {
		return nil, err
	}
	if runState.Version != 1 {
		return nil, fmt.Errorf("unsupported checkpoint RunState version %d", runState.Version)
	}
	if runState.PreparedInputs < 0 || runState.PreparedInputs > len(runState.Consumed) {
		return nil, fmt.Errorf("invalid prepared input cursor %d", runState.PreparedInputs)
	}
	switch runState.Phase {
	case types.PhaseCompleted, types.PhaseFailed, types.PhaseInterrupted:
		return nil, fmt.Errorf("checkpoint run is terminal: %s", runState.Phase)
	}
	return &runState, nil
}

func (snapshotState *snapshotState) encodeSnapshot() ([]byte, error) {
	var err error
	snapshotState.localStateFields["JSONValue"], err = json.Marshal(snapshotState.runStateFields)
	if err != nil {
		return nil, err
	}
	snapshotState.graphFields["State"], err = json.Marshal(snapshotState.localStateFields)
	if err != nil {
		return nil, err
	}
	snapshotState.snapshotFields["MapValues"], err = json.Marshal(snapshotState.graphFields)
	if err != nil {
		return nil, err
	}
	return json.Marshal(snapshotState.snapshotFields)
}

// ReadRunID 从快照中的 RunState 获取原 Run 身份，供未指定 RunID 的恢复使用。
func ReadRunID(snapshot []byte, threadID string) (string, error) {
	snapshotState, err := decodeSnapshot(snapshot)
	if err != nil {
		return "", err
	}
	runState, err := snapshotState.decodeActiveRunState()
	if err != nil {
		return "", err
	}
	if runState.ThreadID != threadID || runState.RunID == "" {
		return "", fmt.Errorf("checkpoint identity mismatch")
	}
	return runState.RunID, nil
}

// ValidateResume checks explicit targets against Eino's persisted interrupt
// addresses. No IDs means the existing before/after-node resume operation.
func ValidateResume(snapshot []byte, interruptIDs []string, resumeData map[string]any) error {
	if len(interruptIDs) == 0 && len(resumeData) == 0 {
		return nil
	}
	var checkpoint interruptTree
	decodeErr := json.Unmarshal(snapshot, &checkpoint)
	if decodeErr != nil {
		return decodeErr
	}
	knownInterruptIDs := map[string]bool{}
	var collectInterruptIDs func(*interruptTree)
	collectInterruptIDs = func(value *interruptTree) {
		if value == nil {
			return
		}
		addresses := value.MapValues["InterruptID2Addr"]
		if addresses != nil {
			for key := range addresses.MapValues {
				knownInterruptIDs[key] = true
			}
		}
		children := value.MapValues["SubGraphs"]
		if children != nil {
			for _, child := range children.MapValues {
				collectInterruptIDs(child)
			}
		}
	}
	collectInterruptIDs(&checkpoint)
	requestedInterruptIDs := map[string]bool{}
	for _, id := range interruptIDs {
		requestedInterruptIDs[id] = true
	}
	for id := range resumeData {
		requestedInterruptIDs[id] = true
	}
	for id := range requestedInterruptIDs {
		if id == "" || !knownInterruptIDs[strconv.Quote(id)] {
			return fmt.Errorf("resume interrupt %q not present in checkpoint", id)
		}
	}
	return nil
}

// 结果未知的工具调用不能重放，但仍允许在原快照中保存已接受的输入。
func rejectUnknownToolOutcome(runState *types.RunState) error {
	for _, call := range runState.Calls {
		if call.Status == types.CallOutcomeUnknown || call.Status == types.CallRunning {
			return &ToolOutcomeUnknownError{CallID: call.Call.ID, Inputs: runState.Consumed}
		}
	}
	return nil
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
