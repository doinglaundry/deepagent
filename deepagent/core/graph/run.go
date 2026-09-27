package graph

import (
	"context"
	"errors"
	"fmt"
	"time"

	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func (a *DeepAgent) execute(ctx context.Context, input []*schema.Message, opts ...RunOptionFunc) (result *schema.Message, err error) {
	options := RunOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(&options)
		}
	}
	ctx, err = a.claimRun(ctx)
	if err != nil {
		return nil, err
	}
	var state *types.RunState
	initialCheckpointSaved := false
	defer func() {
		if state != nil {
			err = a.finishRun(ctx, state, options, initialCheckpointSaved, err)
		}
		a.mu.Lock()
		a.cancel()
		a.active = false
		a.interrupt = nil
		close(a.done)
		a.mu.Unlock()
	}()

	err = a.startRun(ctx, options)
	if err != nil {
		return nil, err
	}
	state = a.newRunState(input, options)
	if len(options.ResumeInterruptIDs) > 0 {
		ctx = compose.Resume(ctx, options.ResumeInterruptIDs...)
	}
	if len(options.ResumeData) > 0 {
		ctx = compose.BatchResumeWithData(ctx, options.ResumeData)
	}
	ctx = context.WithValue(ctx, "deep_agent", a)
	ctx = types.NewStateContext(ctx, a.graphState)

	err = a.beforeRun(ctx, state)
	if err != nil {
		return nil, err
	}
	result, initialCheckpointSaved, err = a.invokeGraph(ctx, state, options)
	return result, err
}

// Reserve execution before storage or resource work, so Close can cancel and wait.
func (a *DeepAgent) claimRun(ctx context.Context) (context.Context, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.active {
		return ctx, errors.New("agent is closed or already running")
	}
	a.active = true
	a.state = nil
	a.done = make(chan struct{})
	ctx, a.cancel = context.WithCancel(ctx)
	ctx, a.interrupt = compose.WithGraphInterrupt(ctx)
	return ctx, nil
}

func (a *DeepAgent) startRun(ctx context.Context, options RunOptions) error {
	runID, err := a.resolveRunID(ctx, options)
	if err != nil {
		return err
	}
	rebuild := a.started || runID != a.runID
	a.runID = runID
	if rebuild {
		err = a.closeResources(ctx)
		if err != nil {
			return err
		}
		err = a.configureRun(ctx)
		if err != nil {
			return err
		}
	}

	a.started = true
	a.executor = newToolExecutor(a.runID, a.tools, a.cfg.Parallelism, a.policy)
	return nil
}

func (a *DeepAgent) newRunState(input []*schema.Message, options RunOptions) *types.RunState {
	state := &types.RunState{Version: 1, ThreadID: a.cfg.ThreadID, RunID: a.runID, AgentName: a.cfg.Name, Depth: a.cfg.Depth, Phase: types.PhasePreparing}
	for i, message := range input {
		if message != nil {
			entry := types.Input{Message: message}
			if i < len(options.InputMeta) {
				entry.Meta = options.InputMeta[i]
			}
			state.Consumed = append(state.Consumed, entry)
		}
	}

	return state
}

func (a *DeepAgent) beforeRun(ctx context.Context, state *types.RunState) error {
	for _, mw := range a.middlewares {
		err := mw.BeforeAgent(ctx)
		if err != nil {
			return err
		}
		lifecycle, ok := mw.(middleware.RunMiddleware)
		if ok {
			err = lifecycle.BeforeRun(ctx, state)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// AfterRun precedes resource cleanup, persistence, and the final event.
func (a *DeepAgent) finishRun(ctx context.Context, state *types.RunState, options RunOptions, initialCheckpointSaved bool, err error) error {
	current := a.state
	if current == nil {
		current = state
	}
	for i := len(a.middlewares) - 1; i >= 0; i-- {
		mw, ok := a.middlewares[i].(middleware.RunMiddleware)
		if ok {
			err = errors.Join(err, mw.AfterRun(ctx, current, err))
		}
	}
	cleanupErr := a.executor.cancel(context.Background())
	err = errors.Join(err, cleanupErr, a.closeResources(ctx))
	// A terminal failure must not discard accepted inputs embedded in a
	// checkpoint. Interrupts retain them in the Eino snapshot.
	_, interrupted := compose.ExtractInterruptInfo(err)
	if err != nil && !interrupted {
		pending := hasPendingInputs(current)
		if pending {
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			err = errors.Join(err, a.persistInputs(saveCtx, current))
			cancel()
		}
	}
	markRunError(ctx, current, err)
	_, interrupted = compose.ExtractInterruptInfo(err)
	// Only finalize a state actually entered by the Graph. A rejected resume
	// or a BeforeRun failure must not overwrite the saved state with a new one.
	if !interrupted && a.state != nil && a.cfg.CheckpointStore != nil && (!options.ForceNewRun || initialCheckpointSaved) {
		checkpointID := options.outputCheckpointID()
		if checkpointID != "" {
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			required := current != state && checkpointID == options.CheckpointID
			saveErr := checkpointer.New(a.cfg.CheckpointStore, a.cfg.ThreadID, a.runID, "core-graph-v1").Finalize(saveCtx, checkpointID, current, required)
			cancel()
			if saveErr != nil {
				err = errors.Join(err, fmt.Errorf("persist terminal checkpoint: %w", saveErr))
			}
		}
	}
	if err == nil {
		err = a.event(ctx, current, "turn_end", "", nil)
	}
	markRunError(ctx, current, err)

	return err
}
