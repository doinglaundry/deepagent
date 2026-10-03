package execution

import (
	"context"
	"eino-cli/deepagent/graph/conversation"
	"eino-cli/deepagent/graph/types"
	"fmt"
	"github.com/cloudwego/eino/schema"
)

func (graph *Graph) persistInputs(ctx context.Context, runState *types.RunState) error {
	if runState.PreparedInputs < 0 || runState.PreparedInputs > len(runState.Consumed) {
		return fmt.Errorf("invalid prepared input cursor")
	}
	for _, input := range runState.Consumed[runState.PreparedInputs:] {
		err := graph.conversation.AddHistory(ctx, runState.RunID, input.Message)
		if err != nil {
			return err
		}
		runState.PreparedInputs++
		err = graph.emitEvent(ctx, runState, "input_consumed", "", input)
		if err != nil {
			return err
		}
	}
	return nil
}

func (graph *Graph) prepareConversation(ctx context.Context, runState *types.RunState) (*types.RunState, error) {
	err := graph.persistInputs(ctx, runState)
	if err != nil {
		return nil, err
	}
	err = graph.compactContext(ctx, runState)
	if err != nil {
		return nil, err
	}
	runState.Phase = types.PhaseModeling
	return runState, nil
}

func (graph *Graph) compactContext(ctx context.Context, runState *types.RunState) error {
	if !graph.conversation.NeedsCompaction(ctx) {
		return nil
	}
	err := graph.emitEvent(ctx, runState, "context_compact_started", "", conversation.ContextCompactStartedPayload{ContextUsage: graph.conversation.GetContextUsage()})
	if err != nil {
		return err
	}
	payload, err := graph.conversation.Compact(ctx, runState.RunID)
	if err != nil {
		return err
	}
	if payload != nil {
		return graph.emitEvent(ctx, runState, "context_compacted", "", *payload)
	}
	return nil
}

func hasPendingInputs(runState *types.RunState) bool {
	return runState.PreparedInputs < len(runState.Consumed)
}

func (graph *Graph) continueRun(ctx context.Context, runState *types.RunState) (*types.RunState, error) {
	if hasPendingInputs(runState) {
		runState.Calls = nil
		runState.Phase = types.PhasePreparing
		return runState, nil
	}

	if graph.config.DrainInput != nil {
		pendingInputs, shouldContinue, err := graph.config.DrainInput(ctx, runState.RunID)
		if err != nil {
			return nil, err
		}
		if shouldContinue && len(pendingInputs) == 0 {
			return nil, fmt.Errorf("drain input returned continuation without input")
		}
		if len(pendingInputs) > 0 {
			before := len(runState.Consumed)
			runState.Consumed = types.AppendInputs(runState.Consumed, pendingInputs...)
			if len(runState.Consumed) == before {
				runState.Phase = types.PhaseCompleted
				return runState, nil
			}
			runState.Calls = nil
			runState.Phase = types.PhasePreparing
			return runState, nil
		}
	}
	runState.Phase = types.PhaseCompleted
	return runState, nil
}

func (graph *Graph) executeFinishNode(ctx context.Context, runState *types.RunState) (*schema.Message, error) {
	runState.Pending = nil
	historyMessages := graph.conversation.GetHistory(ctx)
	if len(historyMessages) == 0 {
		return nil, fmt.Errorf("agent produced no message")
	}
	message := historyMessages[len(historyMessages)-1]
	if message.Role == schema.Tool {
		message = types.CopyMessage(message)
		message.Role = schema.Assistant
		message.ToolCallID = ""
		if message.Content == "" {
			for _, part := range message.UserInputMultiContent {
				if part.Type == schema.ChatMessagePartTypeText {
					message.Content += part.Text
				}
			}
		}

	}
	return message, nil
}
