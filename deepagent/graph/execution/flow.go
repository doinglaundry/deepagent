package execution

import (
	"context"
	checkpointer "eino-cli/deepagent/graph/checkpoint"
	"eino-cli/deepagent/graph/types"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func (a *Graph) buildGraph(ctx context.Context) error {
	g := compose.NewGraph[*types.RunState, *schema.Message](compose.WithGenLocalState(a.newLocalState))

	prepare := compose.InvokableLambda(a.prepareNode)
	err := g.AddLambdaNode("prepare", prepare)
	if err != nil {
		return err
	}
	model := compose.InvokableLambda(a.modelNode)
	err = g.AddLambdaNode("model", model)
	if err != nil {
		return err
	}
	toolNode := compose.InvokableLambda(a.toolsNode)
	err = g.AddLambdaNode("tools", toolNode)
	if err != nil {
		return err
	}
	continuation := compose.InvokableLambda(a.continueNode)
	err = g.AddLambdaNode("continue", continuation)
	if err != nil {
		return err
	}
	finish := compose.InvokableLambda(a.finishNode)
	err = g.AddLambdaNode("finish", finish)
	if err != nil {
		return err
	}

	err = a.connectGraph(g)
	if err != nil {
		return err
	}
	a.graph, err = g.Compile(ctx, a.graphCompileOptions()...)
	return err
}

func (a *Graph) connectGraph(g *compose.Graph[*types.RunState, *schema.Message]) error {
	err := g.AddEdge(compose.START, "prepare")
	if err != nil {
		return err
	}
	err = g.AddEdge("prepare", "model")
	if err != nil {
		return err
	}
	err = g.AddEdge("finish", compose.END)
	if err != nil {
		return err
	}

	err = g.AddBranch("model", compose.NewGraphBranch(a.routeAfterModel, map[string]bool{"tools": true, "continue": true}))
	if err != nil {
		return err
	}
	err = g.AddBranch("tools", compose.NewGraphBranch(a.routeAfterTools, map[string]bool{"model": true, "continue": true}))
	if err != nil {
		return err
	}
	err = g.AddBranch("continue", compose.NewGraphBranch(a.routeAfterContinue, map[string]bool{"prepare": true, "finish": true}))
	return err
}

func (a *Graph) routeAfterModel(_ context.Context, state *types.RunState) (string, error) {
	if len(state.Calls) > 0 {
		return "tools", nil
	}
	return "continue", nil
}

func (a *Graph) routeAfterTools(_ context.Context, state *types.RunState) (string, error) {
	for _, call := range state.Calls {
		if call.Result != nil && call.Result.ReturnDirect {
			return "continue", nil
		}
	}
	return "model", nil
}

func (a *Graph) routeAfterContinue(_ context.Context, state *types.RunState) (string, error) {
	if state.Phase == types.PhasePreparing {
		return "prepare", nil
	}
	return "finish", nil
}

func (a *Graph) graphCompileOptions() []compose.GraphCompileOption {
	options := []compose.GraphCompileOption{
		compose.WithGraphName("deepagent"),
		compose.WithNodeTriggerMode(compose.AnyPredecessor),
		compose.WithMaxRunSteps(a.cfg.MaxSteps),
	}
	if a.cfg.CheckpointStore != nil {
		store := checkpointer.New(a.cfg.CheckpointStore, a.cfg.ThreadID, a.runID, "core-graph-v1")
		options = append(options, compose.WithCheckPointStore(store))
	}
	return options
}

func (a *Graph) modelNode(ctx context.Context, _ *types.RunState) (*types.RunState, error) {
	ctx, state, err := a.enterNode(ctx)
	if err != nil {
		return nil, err
	}
	output, nodeErr := a.callModel(ctx, state)
	return a.leaveNode(ctx, state, output, nodeErr)
}

func (a *Graph) prepareNode(ctx context.Context, _ *types.RunState) (*types.RunState, error) {
	err := a.ensureInitialCheckpoint(ctx)
	if err != nil {
		return nil, err
	}
	ctx, state, err := a.enterNode(ctx)
	if err != nil {
		return nil, err
	}
	output, nodeErr := a.prepare(ctx, state)
	return a.leaveNode(ctx, state, output, nodeErr)
}

func (a *Graph) toolsNode(ctx context.Context, _ *types.RunState) (*types.RunState, error) {
	ctx, state, err := a.enterNode(ctx)
	if err != nil {
		return nil, err
	}
	output, nodeErr := a.callTools(ctx, state)
	return a.leaveNode(ctx, state, output, nodeErr)
}

func (a *Graph) continueNode(ctx context.Context, _ *types.RunState) (*types.RunState, error) {
	ctx, state, err := a.enterNode(ctx)
	if err != nil {
		return nil, err
	}
	output, nodeErr := a.continueRun(ctx, state)
	return a.leaveNode(ctx, state, output, nodeErr)
}

// Eino uses this value for a new run. A resumed run uses its checkpoint state.
func (a *Graph) newLocalState(ctx context.Context) *types.RunState {
	state := types.RunStateFromContext(ctx)
	if state != nil {
		return state
	}
	return &types.RunState{}
}

// The first prepare establishes a durable Eino cursor before doing any work.
func (a *Graph) ensureInitialCheckpoint(ctx context.Context) error {
	initial, ok := ctx.Value(initialCheckpointKey{}).(*types.RunState)
	if !ok {
		return nil
	}
	fresh := false
	err := compose.ProcessState[*types.RunState](ctx, func(_ context.Context, state *types.RunState) error {
		fresh = state == initial
		return nil
	})
	if err != nil {
		return err
	}
	if !fresh {
		return nil
	}
	a.mu.Lock()
	a.state = initial
	a.mu.Unlock()
	return compose.Interrupt(ctx, &initialCheckpoint{})
}

// enterNode always uses Eino's local state, including the restored state.
func (a *Graph) enterNode(ctx context.Context) (context.Context, *types.RunState, error) {
	state, err := a.localState(ctx)
	if err != nil {
		return ctx, nil, err
	}
	cancelled, _ := ctx.Value(approvalCancelKey{}).(bool)
	if cancelled {
		// Restore input ownership first, then cancel the whole Run before any
		// budget check or child/tool dispatch can replay its work.
		return ctx, state, context.Canceled
	}
	if a.cfg.MaxSteps > 0 && state.GraphSteps >= a.cfg.MaxSteps {
		return ctx, nil, fmt.Errorf("maximum graph steps exceeded: %d", a.cfg.MaxSteps)
	}
	state.GraphSteps++
	ctx = types.WithRunState(ctx, state)
	return ctx, state, nil
}

func (a *Graph) leaveNode(ctx context.Context, state, output *types.RunState, nodeErr error) (*types.RunState, error) {
	if a.cfg.Depth > 0 {
		history, err := json.Marshal(a.conversation.History(ctx))
		if err != nil {
			return nil, err
		}
		if state.Extensions == nil {
			state.Extensions = map[string]json.RawMessage{}
		}
		state.Extensions["child_history"] = history

	}
	contextSnapshot := a.conversation.SnapshotContext()
	state.Context = &contextSnapshot
	a.executor.snapshotChildCheckpoints(state)
	markRunError(ctx, state, nodeErr)
	snapshotErr := a.graphState.SnapshotExtensions(state)
	if snapshotErr != nil {
		state.Phase = types.PhaseFailed
		return nil, snapshotErr
	}
	return output, nodeErr
}
