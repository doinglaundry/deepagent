package graph

import (
	"context"
	"fmt"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

func (a *DeepAgent) continueNode(ctx context.Context, _ *types.RunState) (*types.RunState, error) {
	ctx, state, err := a.enterNode(ctx)
	if err != nil {
		return nil, err
	}
	output, nodeErr := a.continueRun(ctx, state)
	return a.leaveNode(ctx, state, output, nodeErr)
}

func hasPendingInputs(s *types.RunState) bool {
	_, pending := s.Extensions["pending_inputs"]
	return pending
}

func (a *DeepAgent) continueRun(ctx context.Context, s *types.RunState) (*types.RunState, error) {
	if hasPendingInputs(s) {
		s.Calls = nil
		s.Phase = types.PhasePreparing
		return s, nil
	}

	if a.drainInput != nil {
		inputs, more, err := a.drainInput(ctx, s.RunID)
		if err != nil {
			return nil, err
		}
		if more && len(inputs) == 0 {
			return nil, fmt.Errorf("drain input returned continuation without input")
		}
		if len(inputs) > 0 {
			before := len(s.Consumed)
			s.Consumed = types.AppendInputs(s.Consumed, inputs...)
			if len(s.Consumed) == before {
				s.Phase = types.PhaseCompleted
				return s, nil
			}
			s.Calls = nil
			s.Phase = types.PhasePreparing
			return s, nil
		}
	}
	s.Phase = types.PhaseCompleted
	return s, nil
}

func (a *DeepAgent) finishNode(ctx context.Context, s *types.RunState) (*schema.Message, error) {
	s.Pending = nil
	history := a.conversation.History(ctx)
	if len(history) == 0 {
		return nil, fmt.Errorf("agent produced no message")
	}
	message := history[len(history)-1]
	if message.Role == schema.Tool {
		message = CopyMessage(message)
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
