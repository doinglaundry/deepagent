package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	checkpointer "eino-cli/deepagent/graph/checkpoint"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func (graph *Graph) savePendingInterrupts(ctx context.Context, checkpointID string, interruptInfo *compose.InterruptInfo) error {
	if graph.runState == nil || len(interruptInfo.InterruptContexts) == 0 {
		return nil
	}
	pendingInterrupts := make([]types.Interrupt, 0, len(interruptInfo.InterruptContexts))
	for _, interrupt := range interruptInfo.InterruptContexts {
		if interrupt == nil {
			continue
		}
		data, err := json.Marshal(interrupt.Info)
		if err != nil {
			return err
		}
		item := types.Interrupt{InterruptID: interrupt.ID, CheckpointID: checkpointID, Kind: "custom", Data: data}
		switch value := interrupt.Info.(type) {
		case *tools.ApprovalInfo:
			item.Kind = "approval"
			item.CallID = value.CallID
		case *tools.FollowUpInfo:
			item.Kind = "follow_up"
		}
		if item.CallID == "" {
			// A single suspended call is unambiguous. Nested/parallel contexts keep
			// their Eino identity; never guess among multiple blocked calls.
			for _, call := range graph.runState.Calls {
				if call.Status == types.CallBlocked {
					if item.CallID != "" {
						item.CallID = ""
						break
					}
					item.CallID = call.Call.ID
				}
			}
		}
		pendingInterrupts = append(pendingInterrupts, item)
	}
	if graph.config.CheckpointStore != nil && checkpointID != "" {
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err := checkpointer.NewGraphStore(graph.config.CheckpointStore, graph.config.ThreadID, graph.runState.RunID).SaveInterrupts(saveCtx, checkpointID, pendingInterrupts)
		if err != nil {
			return fmt.Errorf("persist pending interrupt metadata: %w", err)
		}
	}
	graph.runState.Pending = pendingInterrupts
	return nil
}

// This private interrupt creates Eino's real execution cursor before prepare.
// Run resumes it internally, within the same lifecycle.
type initialCheckpoint struct{}

type initialCheckpointKey struct{}

type approvalCancelKey struct{}

func init() { schema.RegisterName[*initialCheckpoint]("deepagent_initial_checkpoint_v1") }

func (graph *Graph) invokeGraph(ctx context.Context, runState *types.RunState, runOptions RunOptions) (result *messagepkg.Message, initialCheckpointSaved bool, err error) {
	invokeOptions := append([]compose.Option(nil), runOptions.composeOpts...)
	if len(graph.config.Callbacks) > 0 {
		invokeOptions = append(invokeOptions, compose.WithCallbacks(graph.config.Callbacks...))
	}
	if runOptions.CheckpointID != "" {
		invokeOptions = append(invokeOptions, compose.WithCheckPointID(runOptions.CheckpointID))
	}
	if runOptions.WriteToCheckpointID != "" {
		invokeOptions = append(invokeOptions, compose.WithWriteToCheckPointID(runOptions.WriteToCheckpointID))
	}
	if runOptions.ForceNewRun {
		invokeOptions = append(invokeOptions, compose.WithForceNewRun())
	}
	if graph.config.CheckpointStore != nil && runOptions.CheckpointID != "" && (runOptions.WriteToCheckpointID == "" || runOptions.WriteToCheckpointID == runOptions.CheckpointID) {
		ctx = context.WithValue(ctx, initialCheckpointKey{}, runState)
		store := checkpointer.NewGraphStore(graph.config.CheckpointStore, graph.config.ThreadID, graph.runID)
		var fenceMu sync.Mutex
		graph.toolExecutor.persistToolExecutionFence = func(ctx context.Context, call types.ToolCall) error {
			fenceMu.Lock()
			defer fenceMu.Unlock()
			// Fresh local state is the input pointer returned by our Eino state
			// generator. A restored state is decoded from the checkpoint instead.
			restored := types.GetRunState(ctx) != runState
			err := store.MarkToolOutcomeUnknown(ctx, runOptions.CheckpointID, call, restored)
			if err != nil {
				return fmt.Errorf("persist tool execution fence: %w", err)
			}
			return nil
		}
	}

	result, err = graph.runnable.Invoke(types.WithRunState(ctx, runState), runState, invokeOptions...)
	info, interrupted := compose.ExtractInterruptInfo(err)
	if interrupted && len(info.InterruptContexts) == 1 {
		_, initial := info.InterruptContexts[0].Info.(*initialCheckpoint)
		if initial {
			initialCheckpointSaved = true
			// This is a persistence boundary, not a user-visible blocked Run.
			// Model/tool work and lifecycle hooks have not been repeated.
			resumeCtx := compose.Resume(ctx, info.InterruptContexts[0].ID)
			// In the pinned Eino version the last option supplies ForceNewRun.
			// A checkpoint-ID option resets it, so this invocation loads the new
			// cursor instead of starting another forced run.
			resumeOpts := append(append([]compose.Option(nil), invokeOptions...), compose.WithCheckPointID(runOptions.CheckpointID))
			result, err = graph.runnable.Invoke(types.WithRunState(resumeCtx, runState), runState, resumeOpts...)
		}
	}
	info, interrupted = compose.ExtractInterruptInfo(err)
	if interrupted {
		id := runOptions.getOutputCheckpointID()
		saveErr := graph.savePendingInterrupts(ctx, id, info)
		if saveErr != nil {
			return nil, initialCheckpointSaved, saveErr
		}
	}
	return result, initialCheckpointSaved, err
}

// resolveRunID reads checkpoint identity without holding the Agent mutex.
func (graph *Graph) resolveRunID(ctx context.Context, runOptions RunOptions) (string, error) {
	if graph.config.RunID != "" {
		return graph.config.RunID, nil
	}
	runID := graph.runID
	if runOptions.CheckpointID == "" || graph.config.CheckpointStore == nil || runOptions.ForceNewRun {
		return runID, nil
	}
	raw, exists, err := graph.config.CheckpointStore.Get(ctx, runOptions.CheckpointID)
	if err != nil || !exists {
		return runID, err
	}
	return checkpointer.ReadRunID(raw, graph.config.ThreadID)
}

func (runOptions RunOptions) getOutputCheckpointID() string {
	if runOptions.WriteToCheckpointID != "" {
		return runOptions.WriteToCheckpointID
	}
	return runOptions.CheckpointID
}

// A child writes its Eino checkpoint here during execution. The caller embeds
// the bytes in the parent RunState before propagating interruption, so the
// parent's Eino checkpoint is the only external persistence operation.
type childCheckpointStore struct {
	mu   sync.Mutex
	data []byte
}

func (childCheckpointStore *childCheckpointStore) Get(ctx context.Context, _ string) ([]byte, bool, error) {
	err := ctx.Err()
	if err != nil {
		return nil, false, err
	}
	childCheckpointStore.mu.Lock()
	defer childCheckpointStore.mu.Unlock()
	return append([]byte(nil), childCheckpointStore.data...), len(childCheckpointStore.data) > 0, nil
}

func (childCheckpointStore *childCheckpointStore) Set(ctx context.Context, _ string, data []byte) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	childCheckpointStore.mu.Lock()
	defer childCheckpointStore.mu.Unlock()
	childCheckpointStore.data = append([]byte(nil), data...)
	return nil
}

type toolCallIDKey struct{}

type toolExecutorKey struct{}

func (toolExecutor *toolExecutor) getChildCheckpointStore(callID string) *childCheckpointStore {
	toolExecutor.mu.Lock()
	defer toolExecutor.mu.Unlock()
	if toolExecutor.childCheckpointStoresByCallID == nil {
		toolExecutor.childCheckpointStoresByCallID = map[string]*childCheckpointStore{}
	}
	if toolExecutor.childCheckpointStoresByCallID[callID] == nil {
		toolExecutor.childCheckpointStoresByCallID[callID] = &childCheckpointStore{}
	}
	return toolExecutor.childCheckpointStoresByCallID[callID]
}

func (toolExecutor *toolExecutor) restoreChildCheckpoints(runState *types.RunState) {
	toolExecutor.mu.Lock()
	defer toolExecutor.mu.Unlock()
	toolExecutor.childCheckpointStoresByCallID = map[string]*childCheckpointStore{}
	for key, raw := range runState.Extensions {
		callID, ok := strings.CutPrefix(key, "child_checkpoint/")
		if ok {
			toolExecutor.childCheckpointStoresByCallID[callID] = &childCheckpointStore{data: append([]byte(nil), raw...)}
		}
	}
}

// Only the graph node writes RunState; concurrent child executions write their
// own locked buffers. The executor owns both eager and ordinary task buffers.
func (toolExecutor *toolExecutor) snapshotChildCheckpoints(runState *types.RunState) {
	toolExecutor.mu.Lock()
	defer toolExecutor.mu.Unlock()
	for callID, checkpoint := range toolExecutor.childCheckpointStoresByCallID {
		checkpoint.mu.Lock()
		raw := append([]byte(nil), checkpoint.data...)
		checkpoint.mu.Unlock()
		key := "child_checkpoint/" + callID
		if len(raw) == 0 {
			delete(runState.Extensions, key)
			continue
		}
		if runState.Extensions == nil {
			runState.Extensions = map[string]json.RawMessage{}
		}
		runState.Extensions[key] = raw
	}
}
