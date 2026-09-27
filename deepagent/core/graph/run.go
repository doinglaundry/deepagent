package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

func (a *DeepAgent) execute(ctx context.Context, input []*schema.Message, resume *ResumeOptions, opts ...RunOptionFunc) (result *schema.Message, err error) {
	options := RunOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	if resume != nil {
		options.CheckpointID = resume.CheckpointID
		options.ResumeInterruptIDs = resume.InterruptIDs
		options.ResumeData = resume.Data
	}
	a.mu.Lock()
	if a.closed || a.active || (a.streamDone != nil && options.streamDone != a.streamDone) {
		a.mu.Unlock()
		return nil, errors.New("agent is closed or already running")
	}
	runID := a.runID
	if a.cfg.RunID == "" {
		if a.started {
			runID = uuid.NewString()
		}
		if options.CheckpointID != "" && a.cfg.CheckpointStore != nil && !options.ForceNewRun {
			raw, exists, readErr := a.cfg.CheckpointStore.Get(ctx, options.CheckpointID)
			if readErr != nil {
				a.mu.Unlock()
				return nil, readErr
			}
			if exists {
				var envelope checkpointer.Envelope
				if decodeErr := json.Unmarshal(raw, &envelope); decodeErr != nil {
					a.mu.Unlock()
					return nil, decodeErr
				}
				if envelope.Version == 1 {
					if envelope.ThreadID != a.cfg.ThreadID || envelope.RunID == "" {
						a.mu.Unlock()
						return nil, fmt.Errorf("checkpoint identity mismatch")
					}
					runID = envelope.RunID
				}
			}
		}
	}
	rebuild := a.started || runID != a.runID
	a.runID = runID
	if rebuild {
		if a.resourcesOpen {
			a.resourcesOpen = false
			if err := closeMiddlewareResources(context.WithoutCancel(ctx), a.middlewares); err != nil {
				a.mu.Unlock()
				return nil, err
			}
		}
		if err := a.configureRun(ctx); err != nil {
			a.mu.Unlock()
			return nil, err
		}
	}
	a.started = true
	a.active = true
	a.state = nil
	a.done = make(chan struct{})
	ctx, a.cancel = context.WithCancel(ctx)
	ctx, a.interrupt = compose.WithGraphInterrupt(ctx)
	a.chunk = options.chunk
	a.executor = newToolExecutor(runID, a.registry, a.cfg.Parallelism, a.policy)
	a.executor.middlewares = a.middlewares
	var state *types.RunState
	initialCheckpointSaved := false
	if a.cfg.CheckpointStore != nil && options.CheckpointID != "" && (options.WriteToCheckpointID == "" || options.WriteToCheckpointID == options.CheckpointID) {
		store := checkpointer.New(a.cfg.CheckpointStore, a.cfg.ThreadID, runID, "core-graph-v1")
		var fenceMu sync.Mutex
		a.executor.beforeInvoke = func(ctx context.Context, call types.ToolCall) error {
			fenceMu.Lock()
			defer fenceMu.Unlock()
			// Fresh local state is the input pointer returned by our Eino state
			// generator. A restored state is decoded from the checkpoint instead.
			restored := types.RunStateFromContext(ctx) != state
			if err := store.FenceTool(ctx, options.CheckpointID, call, restored); err != nil {
				return fmt.Errorf("persist tool execution fence: %w", err)
			}
			return nil
		}
	}
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		executor := a.executor
		a.mu.Unlock()
		cleanupErr := executor.cancel(context.Background())
		err = errors.Join(err, cleanupErr, a.closeResources(ctx))
		current := a.state
		if current == nil {
			current = state
		}
		// A terminal failure must not discard accepted inputs embedded in a
		// checkpoint. Interrupts retain them in the Eino snapshot.
		if _, interrupted := compose.ExtractInterruptInfo(err); err != nil && !interrupted && current != nil {
			pending := hasPendingInputs(current)
			if pending {
				saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				err = errors.Join(err, a.persistInputs(saveCtx, current))
				cancel()
			}
		}
		markRunError(ctx, current, err)
		_, interrupted := compose.ExtractInterruptInfo(err)
		// Only finalize a state actually entered by the Graph. A rejected resume
		// or a BeforeRun failure must not overwrite the saved state with a new one.
		if !interrupted && a.state != nil && current != nil && a.cfg.CheckpointStore != nil && (!options.ForceNewRun || initialCheckpointSaved) {
			checkpointID := options.CheckpointID
			if options.WriteToCheckpointID != "" {
				checkpointID = options.WriteToCheckpointID
			}
			if checkpointID != "" {
				saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
				required := current != state && checkpointID == options.CheckpointID
				saveErr := checkpointer.New(a.cfg.CheckpointStore, a.cfg.ThreadID, runID, "core-graph-v1").Finalize(saveCtx, checkpointID, current, required)
				cancel()
				if saveErr != nil {
					err = errors.Join(err, fmt.Errorf("persist terminal checkpoint: %w", saveErr))
				}
			}
		}
		if err == nil && current != nil {
			err = a.event(ctx, current, "turn_end", "", nil)
		}
		markRunError(ctx, current, err)
		a.mu.Lock()
		a.cancel()
		a.active = false
		a.interrupt = nil
		a.chunk = nil
		close(a.done)
		a.mu.Unlock()
	}()
	state = &types.RunState{Version: 1, ThreadID: a.cfg.ThreadID, RunID: runID, AgentName: a.cfg.Name, Depth: a.cfg.Depth, Phase: types.PhasePreparing}
	for i, message := range input {
		if message != nil {
			entry := types.Input{Message: message}
			if i < len(options.InputMeta) {
				entry.Meta = options.InputMeta[i]
			}
			state.Consumed = append(state.Consumed, entry)
		}
	}
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
	if len(options.ResumeInterruptIDs) > 0 {
		ctx = compose.Resume(ctx, options.ResumeInterruptIDs...)
	}
	if len(options.ResumeData) > 0 {
		ctx = compose.BatchResumeWithData(ctx, options.ResumeData)
	}
	ctx = context.WithValue(ctx, "deep_agent", a)
	ctx = types.NewStateContext(ctx, a.graphState)
	defer func() {
		err = errors.Join(err, a.cfg.Hooks.AfterAgent(ctx))
		for i := len(a.middlewares) - 1; i >= 0; i-- {
			if mw, ok := a.middlewares[i].(middleware.RunMiddleware); ok {
				current := a.state
				if current == nil {
					current = state
				}
				err = errors.Join(err, mw.AfterRun(ctx, current, err))
			}
		}
	}()
	for _, mw := range a.middlewares {
		if err := mw.BeforeAgent(ctx); err != nil {
			return nil, err
		}
		if lifecycle, ok := mw.(middleware.RunMiddleware); ok {
			if err := lifecycle.BeforeRun(ctx, state); err != nil {
				return nil, err
			}
		}
	}
	if err := a.cfg.Hooks.BeforeAgent(ctx); err != nil {
		return nil, err
	}
	if a.cfg.CheckpointStore != nil && options.CheckpointID != "" && (options.WriteToCheckpointID == "" || options.WriteToCheckpointID == options.CheckpointID) {
		ctx = context.WithValue(ctx, initialCheckpointKey{}, state)
	}
	result, err = a.graph.Invoke(types.WithRunState(ctx, state), state, invokeOpts...)
	if info, interrupted := compose.ExtractInterruptInfo(err); interrupted && len(info.InterruptContexts) == 1 {
		if _, initial := info.InterruptContexts[0].Info.(*initialCheckpoint); initial {
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
	if info, interrupted := compose.ExtractInterruptInfo(err); interrupted {
		id := options.CheckpointID
		if options.WriteToCheckpointID != "" {
			id = options.WriteToCheckpointID
		}
		if saveErr := a.savePending(ctx, id, info); saveErr != nil {
			return nil, saveErr
		}
	}
	return result, err
}
