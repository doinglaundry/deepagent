package graph

import (
	"context"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
	"sync"
	"time"
)

func (a *DeepAgent) savePending(ctx context.Context, id string, info *compose.InterruptInfo) error {
	if a.state == nil || len(info.InterruptContexts) == 0 {
		return nil
	}
	pending := make([]types.Interrupt, 0, len(info.InterruptContexts))
	for _, interrupt := range info.InterruptContexts {
		if interrupt == nil {
			continue
		}
		data, err := json.Marshal(interrupt.Info)
		if err != nil {
			return err
		}
		item := types.Interrupt{InterruptID: interrupt.ID, CheckpointID: id, Kind: "custom", Data: data}
		switch value := interrupt.Info.(type) {
		case *tools.ApprovalInfo:
			item.Kind = "approval"
			item.CallID = value.CallID
		case *tools.FollowUpInfo:
			item.Kind = "follow_up"
		case *tools.ReviewEditInfo:
			item.Kind = "review_edit"
		}
		if item.CallID == "" {
			// A single suspended call is unambiguous. Nested/parallel contexts keep
			// their Eino identity; never guess among multiple blocked calls.
			for _, call := range a.state.Calls {
				if call.Status == types.CallBlocked {
					if item.CallID != "" {
						item.CallID = ""
						break
					}
					item.CallID = call.Call.ID
				}
			}
		}
		pending = append(pending, item)
	}
	if a.cfg.CheckpointStore != nil && id != "" {
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		err := checkpointer.New(a.cfg.CheckpointStore, a.cfg.ThreadID, a.state.RunID, "core-graph-v1").SavePending(saveCtx, id, pending)
		if err != nil {
			return fmt.Errorf("persist pending interrupt metadata: %w", err)
		}
	}
	a.state.Pending = pending
	return nil
}

// This private interrupt creates Eino's real execution cursor before prepare.
// execute resumes it internally, within the same Run lifecycle.
type initialCheckpoint struct{}
type initialCheckpointKey struct{}

func init() { schema.RegisterName[*initialCheckpoint]("deepagent_initial_checkpoint_v1") }

func (a *DeepAgent) invokeGraph(ctx context.Context, state *types.RunState, options RunOptions) (result *schema.Message, initialCheckpointSaved bool, err error) {
	invokeOpts := append([]compose.Option(nil), options.composeOpts...)
	if len(a.cfg.Callbacks) > 0 {
		invokeOpts = append(invokeOpts, compose.WithCallbacks(a.cfg.Callbacks...))
	}
	if options.CheckpointID != "" {
		invokeOpts = append(invokeOpts, compose.WithCheckPointID(options.CheckpointID))
	}
	if options.WriteToCheckpointID != "" {
		invokeOpts = append(invokeOpts, compose.WithWriteToCheckPointID(options.WriteToCheckpointID))
	}
	if options.ForceNewRun {
		invokeOpts = append(invokeOpts, compose.WithForceNewRun())
	}
	if a.cfg.CheckpointStore != nil && options.CheckpointID != "" && (options.WriteToCheckpointID == "" || options.WriteToCheckpointID == options.CheckpointID) {
		ctx = context.WithValue(ctx, initialCheckpointKey{}, state)
		store := checkpointer.New(a.cfg.CheckpointStore, a.cfg.ThreadID, a.runID, "core-graph-v1")
		var fenceMu sync.Mutex
		a.executor.beforeInvoke = func(ctx context.Context, call types.ToolCall) error {
			fenceMu.Lock()
			defer fenceMu.Unlock()
			// Fresh local state is the input pointer returned by our Eino state
			// generator. A restored state is decoded from the checkpoint instead.
			restored := types.RunStateFromContext(ctx) != state
			err := store.FenceTool(ctx, options.CheckpointID, call, restored)
			if err != nil {
				return fmt.Errorf("persist tool execution fence: %w", err)
			}
			return nil
		}
	}

	result, err = a.graph.Invoke(types.WithRunState(ctx, state), state, invokeOpts...)
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
			resumeOpts := append(append([]compose.Option(nil), invokeOpts...), compose.WithCheckPointID(options.CheckpointID))
			result, err = a.graph.Invoke(types.WithRunState(resumeCtx, state), state, resumeOpts...)
		}
	}
	info, interrupted = compose.ExtractInterruptInfo(err)
	if interrupted {
		id := options.outputCheckpointID()
		saveErr := a.savePending(ctx, id, info)
		if saveErr != nil {
			return nil, initialCheckpointSaved, saveErr
		}
	}
	return result, initialCheckpointSaved, err
}

// resolveRunID reads checkpoint identity without holding the Agent mutex.
func (a *DeepAgent) resolveRunID(ctx context.Context, options RunOptions) (string, error) {
	if a.cfg.RunID != "" {
		return a.cfg.RunID, nil
	}
	runID := a.runID
	if a.started {
		runID = uuid.NewString()
	}
	if options.CheckpointID == "" || a.cfg.CheckpointStore == nil || options.ForceNewRun {
		return runID, nil
	}
	raw, exists, err := a.cfg.CheckpointStore.Get(ctx, options.CheckpointID)
	if err != nil || !exists {
		return runID, err
	}
	var envelope checkpointer.Envelope
	err = json.Unmarshal(raw, &envelope)
	if err != nil {
		return "", err
	}
	if envelope.Version != 1 {
		return "", fmt.Errorf("unsupported checkpoint version %d", envelope.Version)
	}
	if envelope.ThreadID != a.cfg.ThreadID || envelope.RunID == "" {
		return "", fmt.Errorf("checkpoint identity mismatch")
	}
	return envelope.RunID, nil
}

func (o RunOptions) outputCheckpointID() string {
	if o.WriteToCheckpointID != "" {
		return o.WriteToCheckpointID
	}
	return o.CheckpointID
}
