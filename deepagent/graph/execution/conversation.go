package execution

import (
	"context"
	"fmt"

	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/schema"
)

func (graph *Graph) persistInputs(ctx context.Context, runState *agentmodel.RunState) error {
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

func (graph *Graph) prepareConversation(ctx context.Context, runState *agentmodel.RunState) (*agentmodel.RunState, error) {
	err := graph.persistInputs(ctx, runState)
	if err != nil {
		return nil, err
	}
	err = graph.compactContext(ctx, runState)
	if err != nil {
		return nil, err
	}
	runState.Phase = agentmodel.PhaseModeling
	return runState, nil
}

func (graph *Graph) compactContext(ctx context.Context, runState *agentmodel.RunState) error {
	if !graph.conversation.NeedsCompaction(ctx) {
		return nil
	}
	err := graph.emitEvent(ctx, runState, "context_compact_started", "", graph.conversation.GetContextUsage())
	if err != nil {
		return err
	}
	usage, err := graph.conversation.Compact(ctx, runState.RunID)
	if err != nil {
		return err
	}
	if usage != nil {
		return graph.emitEvent(ctx, runState, "context_compacted", "", *usage)
	}
	return nil
}

func hasPendingInputs(runState *agentmodel.RunState) bool {
	return runState.PreparedInputs < len(runState.Consumed)
}

func (graph *Graph) continueRun(ctx context.Context, runState *agentmodel.RunState) (*agentmodel.RunState, error) {
	if hasPendingInputs(runState) {
		runState.Calls = nil
		runState.Phase = agentmodel.PhasePreparing
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
			runState.Consumed = agentmodel.AppendInputs(runState.Consumed, pendingInputs...)
			if len(runState.Consumed) == before {
				runState.Phase = agentmodel.PhaseCompleted
				return runState, nil
			}
			runState.Calls = nil
			runState.Phase = agentmodel.PhasePreparing
			return runState, nil
		}
	}
	runState.Phase = agentmodel.PhaseCompleted
	return runState, nil
}

func (graph *Graph) executeFinishNode(ctx context.Context, runState *agentmodel.RunState) (*agentmodel.Message, error) {
	runState.Pending = nil
	historyMessages := graph.conversation.GetHistory(ctx)
	if len(historyMessages) == 0 {
		return nil, fmt.Errorf("agent produced no message")
	}
	message := historyMessages[len(historyMessages)-1]
	if message.Role == schema.Tool {
		message = agentmodel.CopyMessage(message)
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
