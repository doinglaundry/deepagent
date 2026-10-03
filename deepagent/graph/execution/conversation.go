package execution

import (
	"context"
	"eino-cli/deepagent/graph/conversation"
	"eino-cli/deepagent/graph/types"
	"fmt"
	"github.com/cloudwego/eino/schema"
)

func (a *Graph) persistInputs(ctx context.Context, s *types.RunState) error {
	if s.PreparedInputs < 0 || s.PreparedInputs > len(s.Consumed) {
		return fmt.Errorf("invalid prepared input cursor")
	}
	for _, input := range s.Consumed[s.PreparedInputs:] {
		err := a.conversation.AddHistory(ctx, s.RunID, input.Message)
		if err != nil {
			return err
		}
		s.PreparedInputs++
		err = a.event(ctx, s, "input_consumed", "", input)
		if err != nil {
			return err
		}
	}
	return nil
}

func (a *Graph) prepare(ctx context.Context, s *types.RunState) (*types.RunState, error) {
	err := a.persistInputs(ctx, s)
	if err != nil {
		return nil, err
	}
	err = a.compactContext(ctx, s)
	if err != nil {
		return nil, err
	}
	s.Phase = types.PhaseModeling
	return s, nil
}

func (a *Graph) compactContext(ctx context.Context, s *types.RunState) error {
	if !a.conversation.CompactNeeded(ctx) {
		return nil
	}
	err := a.event(ctx, s, "context_compact_started", "", conversation.ContextCompactStartedPayload{ContextUsage: a.conversation.ContextUsage()})
	if err != nil {
		return err
	}
	payload, err := a.conversation.Compact(ctx, s.RunID)
	if err != nil {
		return err
	}
	if payload != nil {
		return a.event(ctx, s, "context_compacted", "", *payload)
	}
	return nil
}

func hasPendingInputs(s *types.RunState) bool {
	return s.PreparedInputs < len(s.Consumed)
}

func (a *Graph) continueRun(ctx context.Context, s *types.RunState) (*types.RunState, error) {
	if hasPendingInputs(s) {
		s.Calls = nil
		s.Phase = types.PhasePreparing
		return s, nil
	}

	if a.cfg.DrainInput != nil {
		inputs, more, err := a.cfg.DrainInput(ctx, s.RunID)
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

func (a *Graph) finishNode(ctx context.Context, s *types.RunState) (*schema.Message, error) {
	s.Pending = nil
	history := a.conversation.History(ctx)
	if len(history) == 0 {
		return nil, fmt.Errorf("agent produced no message")
	}
	message := history[len(history)-1]
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
