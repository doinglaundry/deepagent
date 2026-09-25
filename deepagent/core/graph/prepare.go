package graph

import (
	"context"
	"encoding/json"
	"fmt"

	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/types"
)

func (a *DeepAgent) prepareNode(ctx context.Context, input *types.RunState) (*types.RunState, error) {
	err := a.ensureInitialCheckpoint(ctx)
	if err != nil {
		return nil, err
	}
	ctx, state, err := a.enterNode(ctx, input)
	if err != nil {
		return nil, err
	}
	output, nodeErr := a.prepare(ctx, state)
	return a.leaveNode(ctx, state, output, nodeErr)
}

func (a *DeepAgent) persistInputs(ctx context.Context, s *types.RunState) error {
	var prepared int
	raw := s.Extensions["prepared_inputs"]
	if len(raw) > 0 {
		err := json.Unmarshal(raw, &prepared)
		if err != nil {
			return err
		}
	}
	if prepared < 0 || prepared > len(s.Consumed) {
		return fmt.Errorf("invalid prepared input cursor")
	}
	for _, input := range s.Consumed[prepared:] {
		err := a.conversation.AddHistory(ctx, s.RunID, input.Message)
		if err != nil {
			return err
		}
		prepared++
		if s.Extensions == nil {
			s.Extensions = make(map[string]json.RawMessage)
		}
		s.Extensions["prepared_inputs"], _ = json.Marshal(prepared)
		err = a.event(ctx, s, "input_consumed", "", input)
		if err != nil {
			return err
		}
	}
	delete(s.Extensions, "pending_inputs")
	return nil
}
func (a *DeepAgent) prepare(ctx context.Context, s *types.RunState) (*types.RunState, error) {
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
func (a *DeepAgent) compactContext(ctx context.Context, s *types.RunState) error {
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
