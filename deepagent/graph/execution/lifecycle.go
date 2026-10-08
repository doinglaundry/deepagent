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

func (graph *Graph) closeResources(ctx context.Context) error {
	graph.mu.Lock()
	resourcesClosing := graph.resourcesClosing
	if resourcesClosing != nil {
		graph.mu.Unlock()
		select {
		case <-resourcesClosing:
			graph.mu.Lock()
			err := graph.resourcesCloseErr
			graph.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if !graph.resourcesOpen {
		graph.mu.Unlock()
		return nil
	}
	graph.resourcesOpen = false
	resourcesClosing = make(chan struct{})
	graph.resourcesClosing = resourcesClosing
	middlewares := graph.middlewares
	graph.mu.Unlock()
	err := closeMiddlewareResources(context.WithoutCancel(ctx), middlewares)
	graph.mu.Lock()
	graph.resourcesCloseErr = err
	graph.resourcesClosing = nil
	close(resourcesClosing)
	graph.mu.Unlock()
	return err
}

func (graph *Graph) emitEvent(ctx context.Context, runState *types.RunState, kind, callID string, data any) error {
	graph.eventMu.Lock()
	defer graph.eventMu.Unlock()
	runState.EventSeq++
	event := types.RuntimeEvent{Sequence: runState.EventSeq, Kind: kind, CallID: callID, Data: data}
	for _, currentMiddleware := range graph.middlewares {
		observer, ok := currentMiddleware.(middleware.EventObserver)
		if ok {
			err := observer.Observe(ctx, event)
			if err != nil {
				return err
			}
		}
	}
	if graph.config.Emit == nil {
		return nil
	}
	return graph.config.Emit(ctx, event)
}

// Reserve execution before storage or resource work, so Close can cancel and wait.
func (graph *Graph) beginInvoke(ctx context.Context) (context.Context, error) {
	graph.mu.Lock()
	defer graph.mu.Unlock()
	if graph.closed || graph.invoked {
		return ctx, errors.New("agent is closed or has already run")
	}
	graph.invoked = true
	graph.invoking = true
	if graph.done == nil {
		graph.done = make(chan struct{})
	}
	if graph.cancel == nil {
		ctx, graph.cancel = context.WithCancelCause(ctx)
	}
	ctx, graph.interrupt = compose.WithGraphInterrupt(ctx)
	return ctx, nil
}

func (graph *Graph) prepareRun(ctx context.Context, runState *types.RunState) error {
	for _, currentMiddleware := range graph.middlewares {
		err := currentMiddleware.PrepareAgent(ctx)
		if err != nil {
			return err
		}
		runMiddleware, ok := currentMiddleware.(middleware.RunMiddleware)
		if ok {
			err = runMiddleware.PrepareRun(ctx, runState)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

// FinishRun precedes resource cleanup, persistence, and the final event.
func (graph *Graph) finishInvoke(ctx context.Context, runState *types.RunState, runOptions RunOptions, initialCheckpointSaved bool, err error) error {
	currentRunState := graph.runState
	if currentRunState == nil {
		currentRunState = runState
	}
	for i := len(graph.middlewares) - 1; i >= 0; i-- {
		currentMiddleware, ok := graph.middlewares[i].(middleware.RunMiddleware)
		if ok {
			err = errors.Join(err, currentMiddleware.FinishRun(ctx, currentRunState, err))
		}
	}
	cleanupErr := graph.toolExecutor.cancelToolExecutions(context.Background())
	err = errors.Join(err, cleanupErr, graph.closeResources(ctx))
	// A terminal failure must not discard accepted inputs embedded in a
	// checkpoint. Interrupts retain them in the Eino snapshot.
	_, interrupted := compose.ExtractInterruptInfo(err)
	if err != nil && !interrupted && graph.runState != nil {
		pending := hasPendingInputs(currentRunState)
		if pending {
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			err = errors.Join(err, graph.persistInputs(saveCtx, currentRunState))
			cancel()
		}
	}
	markRunError(ctx, currentRunState, err)
	_, interrupted = compose.ExtractInterruptInfo(err)
	// Only finalize a state actually entered by the Graph. A rejected resume
	// or a PrepareRun failure must not overwrite the saved state with a new one.
	if !interrupted && graph.runState != nil && graph.config.CheckpointStore != nil && (!runOptions.ForceNewRun || initialCheckpointSaved) {
		checkpointID := runOptions.getOutputCheckpointID()
		if checkpointID != "" {
			saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			required := currentRunState != runState && checkpointID == runOptions.CheckpointID
			saveErr := checkpointer.NewGraphStore(graph.config.CheckpointStore, graph.config.ThreadID, graph.runID).SaveTerminalState(saveCtx, checkpointID, currentRunState, required)
			cancel()
			if saveErr != nil {
				err = errors.Join(err, fmt.Errorf("persist terminal checkpoint: %w", saveErr))
			}
		}
	}
	if err == nil {
		err = graph.emitEvent(ctx, currentRunState, "turn_end", "", nil)
	}
	markRunError(ctx, currentRunState, err)

	return err
}

func (graph *Graph) GetName() string { return graph.config.Name }

func (graph *Graph) GetDepth() int { return graph.config.Depth }

func (graph *Graph) GetGraphState() *types.GraphState { return graph.graphState }

func (graph *Graph) Close(ctx context.Context) error {
	graph.mu.Lock()
	graph.closed = true
	var done chan struct{}
	if graph.invoking {
		graph.cancel(context.Canceled)
		done = graph.done
	}
	graph.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return graph.closeResources(ctx)
}

func (graph *Graph) Interrupt(opts ...compose.GraphInterruptOption) bool {
	graph.mu.Lock()
	defer graph.mu.Unlock()
	if !graph.invoking || graph.interrupt == nil {
		return false
	}
	graph.interrupt(opts...)
	// Eino's interrupt function closes a channel and is one-shot.
	graph.interrupt = nil
	return true
}

// endInvoke releases the invocation gate after Graph resources are cleaned up.
func (graph *Graph) endInvoke(err error) {
	graph.mu.Lock()
	defer graph.mu.Unlock()
	graph.cancel(err)
	graph.invoking = false
	graph.interrupt = nil
	close(graph.done)
}

// GetGraph 从节点上下文中获取当前 Graph。
func GetGraph(ctx context.Context) *Graph {
	contextValue := ctx.Value("graph")
	if contextValue == nil {
		return nil
	}
	graph, ok := contextValue.(*Graph)
	if !ok {
		return nil
	}
	return graph
}

// GetWholeGraphState 获取当前 Agent 的完整图状态。
func GetWholeGraphState(ctx context.Context) *types.GraphState {
	graph := GetGraph(ctx)
	if graph == nil {
		return nil
	}
	return graph.GetGraphState()
}
