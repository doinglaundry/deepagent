package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/compose"
)

func (graph *Graph) newRunState(inputMessages []*agentmodel.Message, runOptions RunOptions) *agentmodel.RunState {
	runState := &agentmodel.RunState{Version: 1, ThreadID: graph.config.ThreadID, RunID: graph.runID, Depth: graph.config.Depth, Phase: agentmodel.PhasePreparing}
	for i, message := range inputMessages {
		if message != nil {
			copy := *message
			copy.ThreadID, copy.RunID = graph.config.ThreadID, graph.runID
			consumedInput := agentmodel.RunInput{MessageID: copy.MessageID, Message: &copy}
			if i < len(runOptions.InputIDs) {
				consumedInput.MessageID = runOptions.InputIDs[i]
				consumedInput.Message.MessageID = consumedInput.MessageID
			}
			if i < len(runOptions.InputMeta) {
				consumedInput.Meta = runOptions.InputMeta[i]
			}
			runState.Consumed = append(runState.Consumed, consumedInput)
		}
	}

	return runState
}

// getLocalState reads the authoritative RunState from Eino, then restores runtime
// collaborators once for each state object seen by this agent.
func (graph *Graph) getLocalState(ctx context.Context) (*agentmodel.RunState, error) {
	var runState *agentmodel.RunState
	err := compose.ProcessState[*agentmodel.RunState](ctx, func(_ context.Context, localRunState *agentmodel.RunState) error {
		if localRunState.PreparedInputs < 0 || localRunState.PreparedInputs > len(localRunState.Consumed) {
			return fmt.Errorf("invalid prepared input cursor")
		}
		if localRunState.GraphSteps < 0 || localRunState.ModelCalls < 0 {
			return fmt.Errorf("invalid negative run budget counters")
		}
		runState = localRunState
		return nil
	})
	if err != nil {
		return nil, err
	}
	graph.mu.Lock()
	needsRestore := graph.runState != runState
	graph.runState = runState
	graph.mu.Unlock()
	if needsRestore {
		err = graph.restoreLocalState(ctx, runState)
		if err != nil {
			return nil, err
		}
	}
	return runState, nil
}

func (graph *Graph) restoreLocalState(ctx context.Context, runState *agentmodel.RunState) error {
	err := graph.restoreChildConversation(ctx, runState)
	if err != nil {
		return err
	}
	if runState.ContextUsage != nil {
		err = graph.conversation.RestoreContext(ctx, runState.HistorySeq, *runState.ContextUsage)
		if err != nil {
			return err
		}
	}
	err = graph.graphState.RestoreExtensions(runState)
	if err != nil {
		return err
	}
	err = graph.conversation.RestoreRunUsage(ctx, runState.Usage)
	if err != nil {
		return err
	}
	graph.toolExecutor.restoreToolExecutions(runState.Calls)
	graph.toolExecutor.restoreChildCheckpoints(runState)
	graph.toolExecutor.onToolStart = func(ctx context.Context, toolCallState agentmodel.ToolCallState) error {
		return graph.emitEvent(ctx, runState, "tool_start", toolCallState.Call.ID, agentmodel.ToolStartPayload{Name: toolCallState.Call.Name, CallID: toolCallState.Call.ID, Args: toolCallState.Call.Arguments, ToolStartTime: toolCallState.StartedAt})
	}
	return graph.emitEvent(ctx, runState, "run_state_restored", "", runState.Consumed)
}

func (graph *Graph) restoreChildConversation(ctx context.Context, runState *agentmodel.RunState) error {
	if graph.config.Depth <= 0 {
		return nil
	}
	historyJSON, ok := runState.Extensions["child_history"]
	if !ok {
		return nil
	}
	var historyMessages []*agentmodel.Message
	err := json.Unmarshal(historyJSON, &historyMessages)
	if err != nil {
		return err
	}
	err = graph.conversation.AddHistory(ctx, runState.RunID, historyMessages...)
	if err != nil {
		return err
	}
	return nil
}

// Node errors set checkpoint-visible state. The final execution error is
// applied again after checkpoint saving and resource cleanup have completed.
func markRunError(ctx context.Context, runState *agentmodel.RunState, err error) {
	if runState == nil || err == nil {
		return
	}
	_, interrupt := compose.IsInterruptRerunError(err)
	_, nested := compose.ExtractInterruptInfo(err)
	if interrupt || nested {
		runState.Phase = agentmodel.PhaseBlocked
	} else if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		runState.Phase = agentmodel.PhaseInterrupted
	} else {
		runState.Phase = agentmodel.PhaseFailed
	}
}
