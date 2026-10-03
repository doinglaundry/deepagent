package execution

import (
	"context"
	checkpointer "eino-cli/deepagent/graph/checkpoint"
	"eino-cli/deepagent/graph/middleware"
	"eino-cli/deepagent/graph/types"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/compose"
	"time"
)

func closeMiddlewareResources(ctx context.Context, middlewares []middleware.Middleware) error {
	var err error
	for i := len(middlewares) - 1; i >= 0; i-- {
		closer, ok := middlewares[i].(middleware.ResourceCloser)
		if ok {
			err = errors.Join(err, closer.Close(ctx))
		}
	}
	return err
}

func (a *Graph) closeResources(ctx context.Context) error {
	a.mu.Lock()
	closing := a.resourcesClosing
	if closing != nil {
		a.mu.Unlock()
		select {
		case <-closing:
			a.mu.Lock()
			err := a.resourcesCloseErr
			a.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if !a.resourcesOpen {
		a.mu.Unlock()
		return nil
	}
	a.resourcesOpen = false
	closing = make(chan struct{})
	a.resourcesClosing = closing
	middlewares := a.middlewares
	a.mu.Unlock()
	err := closeMiddlewareResources(context.WithoutCancel(ctx), middlewares)
	a.mu.Lock()
	a.resourcesCloseErr = err
	a.resourcesClosing = nil
	close(closing)
	a.mu.Unlock()
	return err
}

func (a *Graph) event(ctx context.Context, state *types.RunState, kind, callID string, data any) error {
	a.eventMu.Lock()
	defer a.eventMu.Unlock()
	state.EventSeq++
	event := types.RuntimeEvent{Sequence: state.EventSeq, Kind: kind, CallID: callID, Data: data}
	for _, mw := range a.middlewares {
		observer, ok := mw.(middleware.EventObserver)
		if ok {
			err := observer.Observe(ctx, event)
			if err != nil {
				return err
			}
		}
	}
	if a.cfg.Emit == nil {
		return nil
	}
	return a.cfg.Emit(ctx, event)
}

// Reserve execution before storage or resource work, so Close can cancel and wait.
func (a *Graph) beginInvoke(ctx context.Context) (context.Context, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.invoked {
		return ctx, errors.New("agent is closed or has already run")
	}
	a.invoked = true
	a.invoking = true
	if a.done == nil {
		a.done = make(chan struct{})
	}
	if a.cancel == nil {
		ctx, a.cancel = context.WithCancelCause(ctx)
	}
	ctx, a.interrupt = compose.WithGraphInterrupt(ctx)
	return ctx, nil
}

func (a *Graph) beforeRun(ctx context.Context, state *types.RunState) error {
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
func (a *Graph) finishInvoke(ctx context.Context, state *types.RunState, options RunOptions, initialCheckpointSaved bool, err error) error {
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
	if err != nil && !interrupted && a.state != nil {
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

func (a *Graph) Name() string { return a.cfg.Name }

func (a *Graph) Depth() int { return a.cfg.Depth }

func (a *Graph) GraphState() *types.GraphState { return a.graphState }

func (a *Graph) Close(ctx context.Context) error {
	a.mu.Lock()
	a.closed = true
	var done chan struct{}
	if a.invoking {
		a.cancel(context.Canceled)
		done = a.done
	}
	a.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return a.closeResources(ctx)
}

func (a *Graph) Interrupt(opts ...compose.GraphInterruptOption) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.invoking || a.interrupt == nil {
		return false
	}
	a.interrupt(opts...)
	// Eino's interrupt function closes a channel and is one-shot.
	a.interrupt = nil
	return true
}

// endInvoke releases the invocation gate after Graph resources are cleaned up.
func (a *Graph) endInvoke(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.cancel(err)
	a.invoking = false
	a.interrupt = nil
	close(a.done)
}

// GetGraph 从节点上下文中获取当前 Graph。
func GetGraph(ctx context.Context) *Graph {
	ins := ctx.Value("graph")
	if ins == nil {
		return nil
	}
	agent, ok := ins.(*Graph)
	if !ok {
		return nil
	}
	return agent
}

// GetWholeGraphState 获取当前 Agent 的完整图状态。
func GetWholeGraphState(ctx context.Context) *types.GraphState {
	a := GetGraph(ctx)
	if a == nil {
		return nil
	}
	return a.GraphState()
}
