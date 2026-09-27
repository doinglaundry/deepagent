package graph

import (
	"context"
	"eino-cli/deepagent/core/runtime/checkpointer"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func (a *DeepAgent) buildGraph(ctx context.Context) error {
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

func (a *DeepAgent) connectGraph(g *compose.Graph[*types.RunState, *schema.Message]) error {
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

func (a *DeepAgent) routeAfterModel(_ context.Context, state *types.RunState) (string, error) {
	if len(state.Calls) > 0 {
		return "tools", nil
	}
	return "continue", nil
}

func (a *DeepAgent) routeAfterTools(_ context.Context, state *types.RunState) (string, error) {
	for _, call := range state.Calls {
		if call.Result != nil && call.Result.ReturnDirect {
			return "continue", nil
		}
	}
	return "model", nil
}

func (a *DeepAgent) routeAfterContinue(_ context.Context, state *types.RunState) (string, error) {
	if state.Phase == types.PhasePreparing {
		return "prepare", nil
	}
	return "finish", nil
}

func (a *DeepAgent) graphCompileOptions() []compose.GraphCompileOption {
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
