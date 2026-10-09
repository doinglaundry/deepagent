package execution

import (
	"context"
	"encoding/json"
	"fmt"

	checkpointer "eino-cli/deepagent/graph/checkpoint"
	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/compose"
)

func (graph *Graph) buildGraph(ctx context.Context) error {
	einoGraph := compose.NewGraph[*agentmodel.RunState, *agentmodel.Message](compose.WithGenLocalState(graph.newLocalState))

	prepareNode := compose.InvokableLambda(graph.executePrepareNode)
	err := einoGraph.AddLambdaNode("prepare", prepareNode)
	if err != nil {
		return err
	}
	modelNode := compose.InvokableLambda(graph.executeModelNode)
	err = einoGraph.AddLambdaNode("model", modelNode)
	if err != nil {
		return err
	}
	toolsNode := compose.InvokableLambda(graph.executeToolsNode)
	err = einoGraph.AddLambdaNode("tools", toolsNode)
	if err != nil {
		return err
	}
	continueNode := compose.InvokableLambda(graph.executeContinueNode)
	err = einoGraph.AddLambdaNode("continue", continueNode)
	if err != nil {
		return err
	}
	finishNode := compose.InvokableLambda(graph.executeFinishNode)
	err = einoGraph.AddLambdaNode("finish", finishNode)
	if err != nil {
		return err
	}

	err = graph.connectGraphEdges(einoGraph)
	if err != nil {
		return err
	}
	graph.runnable, err = einoGraph.Compile(ctx, graph.buildGraphCompileOptions()...)
	return err
}

func (graph *Graph) connectGraphEdges(einoGraph *compose.Graph[*agentmodel.RunState, *agentmodel.Message]) error {
	err := einoGraph.AddEdge(compose.START, "prepare")
	if err != nil {
		return err
	}
	err = einoGraph.AddEdge("prepare", "model")
	if err != nil {
		return err
	}
	err = einoGraph.AddEdge("finish", compose.END)
	if err != nil {
		return err
	}

	err = einoGraph.AddBranch("model", compose.NewGraphBranch(graph.routeAfterModel, map[string]bool{"tools": true, "continue": true}))
	if err != nil {
		return err
	}
	err = einoGraph.AddBranch("tools", compose.NewGraphBranch(graph.routeAfterTools, map[string]bool{"model": true, "continue": true}))
	if err != nil {
		return err
	}
	err = einoGraph.AddBranch("continue", compose.NewGraphBranch(graph.routeAfterContinue, map[string]bool{"prepare": true, "finish": true}))
	return err
}

func (graph *Graph) routeAfterModel(_ context.Context, runState *agentmodel.RunState) (string, error) {
	if len(runState.Calls) > 0 {
		return "tools", nil
	}
	return "continue", nil
}

func (graph *Graph) routeAfterTools(_ context.Context, runState *agentmodel.RunState) (string, error) {
	for _, call := range runState.Calls {
		if call.Result != nil && call.Result.ReturnDirect {
			return "continue", nil
		}
	}
	return "model", nil
}

func (graph *Graph) routeAfterContinue(_ context.Context, runState *agentmodel.RunState) (string, error) {
	if runState.Phase == agentmodel.PhasePreparing {
		return "prepare", nil
	}
	return "finish", nil
}

func (graph *Graph) buildGraphCompileOptions() []compose.GraphCompileOption {
	compileOptions := []compose.GraphCompileOption{
		compose.WithGraphName("deepagent"),
		compose.WithNodeTriggerMode(compose.AnyPredecessor),
		compose.WithMaxRunSteps(graph.config.MaxSteps),
	}
	if graph.config.CheckpointStore != nil {
		store := checkpointer.NewGraphStore(graph.config.CheckpointStore, graph.config.ThreadID, graph.runID)
		compileOptions = append(compileOptions, compose.WithCheckPointStore(store))
	}
	return compileOptions
}

func (graph *Graph) executeModelNode(ctx context.Context, _ *agentmodel.RunState) (*agentmodel.RunState, error) {
	ctx, runState, err := graph.enterNode(ctx)
	if err != nil {
		return nil, err
	}
	nextRunState, nodeErr := graph.callModel(ctx, runState)
	return graph.leaveNode(ctx, runState, nextRunState, nodeErr)
}

func (graph *Graph) executePrepareNode(ctx context.Context, _ *agentmodel.RunState) (*agentmodel.RunState, error) {
	err := graph.ensureInitialCheckpoint(ctx)
	if err != nil {
		return nil, err
	}
	ctx, runState, err := graph.enterNode(ctx)
	if err != nil {
		return nil, err
	}
	nextRunState, nodeErr := graph.prepareConversation(ctx, runState)
	return graph.leaveNode(ctx, runState, nextRunState, nodeErr)
}

func (graph *Graph) executeToolsNode(ctx context.Context, _ *agentmodel.RunState) (*agentmodel.RunState, error) {
	ctx, runState, err := graph.enterNode(ctx)
	if err != nil {
		return nil, err
	}
	nextRunState, nodeErr := graph.callTools(ctx, runState)
	return graph.leaveNode(ctx, runState, nextRunState, nodeErr)
}

func (graph *Graph) executeContinueNode(ctx context.Context, _ *agentmodel.RunState) (*agentmodel.RunState, error) {
	ctx, runState, err := graph.enterNode(ctx)
	if err != nil {
		return nil, err
	}
	nextRunState, nodeErr := graph.continueRun(ctx, runState)
	return graph.leaveNode(ctx, runState, nextRunState, nodeErr)
}

// Eino uses this value for a new run. A resumed run uses its checkpoint state.
func (graph *Graph) newLocalState(ctx context.Context) *agentmodel.RunState {
	runState := agentmodel.GetRunState(ctx)
	if runState != nil {
		return runState
	}
	return &agentmodel.RunState{}
}

// The first prepare establishes a durable Eino cursor before doing any work.
func (graph *Graph) ensureInitialCheckpoint(ctx context.Context) error {
	initialRunState, ok := ctx.Value(initialCheckpointKey{}).(*agentmodel.RunState)
	if !ok {
		return nil
	}
	isInitialState := false
	err := compose.ProcessState[*agentmodel.RunState](ctx, func(_ context.Context, runState *agentmodel.RunState) error {
		isInitialState = runState == initialRunState
		return nil
	})
	if err != nil {
		return err
	}
	if !isInitialState {
		return nil
	}
	graph.mu.Lock()
	graph.runState = initialRunState
	graph.mu.Unlock()
	return compose.Interrupt(ctx, &initialCheckpoint{})
}

// enterNode always uses Eino's local state, including the restored state.
func (graph *Graph) enterNode(ctx context.Context) (context.Context, *agentmodel.RunState, error) {
	runState, err := graph.getLocalState(ctx)
	if err != nil {
		return ctx, nil, err
	}
	approvalCancelled, _ := ctx.Value(approvalCancelKey{}).(bool)
	if approvalCancelled {
		// Restore input ownership first, then cancel the whole Run before any
		// budget check or child/tool dispatch can replay its work.
		return ctx, runState, context.Canceled
	}
	if graph.config.MaxSteps > 0 && runState.GraphSteps >= graph.config.MaxSteps {
		return ctx, nil, fmt.Errorf("maximum graph steps exceeded: %d", graph.config.MaxSteps)
	}
	runState.GraphSteps++
	ctx = agentmodel.WithRunState(ctx, runState)
	return ctx, runState, nil
}

func (graph *Graph) leaveNode(ctx context.Context, runState, nextRunState *agentmodel.RunState, nodeErr error) (*agentmodel.RunState, error) {
	if graph.config.Depth > 0 {
		historyJSON, err := json.Marshal(graph.conversation.GetHistory(ctx))
		if err != nil {
			return nil, err
		}
		if runState.Extensions == nil {
			runState.Extensions = map[string]json.RawMessage{}
		}
		runState.Extensions["child_history"] = historyJSON

	}
	historySeq, contextTokenUsage := graph.conversation.SnapshotContext()
	runState.HistorySeq = historySeq
	runState.ContextUsage = &contextTokenUsage
	graph.toolExecutor.snapshotChildCheckpoints(runState)
	markRunError(ctx, runState, nodeErr)
	snapshotErr := graph.graphState.SnapshotExtensions(runState)
	if snapshotErr != nil {
		runState.Phase = agentmodel.PhaseFailed
		return nil, snapshotErr
	}
	return nextRunState, nodeErr
}
