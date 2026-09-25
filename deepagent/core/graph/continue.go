package graph

import (
	"context"
	"fmt"

	"eino-cli/deepagent/core/types"
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

	// Consult the compatibility callback before DrainInput seals the thread's
	// acceptance boundary. A continuation stays inside this same graph.
	returnDirect := false
	for _, call := range s.Calls {
		if call.Result != nil && call.Result.ReturnDirect {
			returnDirect = true
			break
		}
	}
	if !returnDirect && a.cfg.ContinueAfterModel != nil {
		more, err := a.cfg.ContinueAfterModel(ctx)
		if err != nil {
			return nil, err
		}
		if more {
			s.Calls = nil
			s.Phase = types.PhasePreparing
			return s, nil
		}
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
			s.Consumed = append(s.Consumed, inputs...)
			s.Calls = nil
			s.Phase = types.PhasePreparing
			return s, nil
		}
	}
	s.Phase = types.PhaseCompleted
	return s, nil
}
