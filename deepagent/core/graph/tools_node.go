package graph

import (
	"context"
	"encoding/json"
	"fmt"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

func (a *DeepAgent) toolsNode(ctx context.Context, _ *types.RunState) (*types.RunState, error) {
	ctx, state, err := a.enterNode(ctx)
	if err != nil {
		return nil, err
	}
	output, nodeErr := a.callTools(ctx, state)
	return a.leaveNode(ctx, state, output, nodeErr)
}

func (a *DeepAgent) callTools(ctx context.Context, s *types.RunState) (*types.RunState, error) {
	s.Phase = types.PhaseTools
	calls := make([]types.ToolCall, len(s.Calls))
	for i := range s.Calls {
		calls[i] = s.Calls[i].Call
	}
	results, err := a.executor.executeBatch(ctx, calls, func(ctx context.Context, call types.ToolCall, chunk string) error {
		return a.event(ctx, s, "tool_call_output_chunk", call.ID, types.ToolOutputChunk{Call: call, Content: chunk})
	})
	a.executor.snapshot(s.Calls)
	// A later call may interrupt this batch. Its first Eino snapshot must not
	// retain an approval obligation already resolved by a completed call.
	pending := s.Pending[:0]
	for _, item := range s.Pending {
		completed := false
		for _, call := range s.Calls {
			if item.CallID != "" && item.CallID == call.Call.ID && call.Status == types.CallCompleted {
				completed = true
				break
			}
		}
		if !completed {
			pending = append(pending, item)
		}
	}
	s.Pending = pending
	if err != nil {
		return nil, err
	}
	byID := make(map[string]types.ToolResult, len(results))
	for _, result := range results {
		byID[result.CallID] = result
	}
	for i := range s.Calls {
		result := byID[s.Calls[i].Call.ID]
		s.Calls[i].Result = &result
		s.Calls[i].Status = types.CallCompleted
	}
	for i, result := range results {
		call := s.Calls[i].Call
		if call.Name == tools.ToolUpdatePlan && !result.IsError {
			var update tools.PlanUpdate
			err := json.Unmarshal([]byte(result.Content), &update)
			if err != nil {
				return nil, fmt.Errorf("decode update_plan result: %w", err)
			}
			err = a.event(ctx, s, "plan_updated", result.CallID, update)
			if err != nil {
				return nil, err
			}
		}
		message := schema.ToolMessage(result.Content, result.CallID)
		if len(result.MultiContent) > 0 {
			message.Content = ""
			message.UserInputMultiContent = result.MultiContent
		}
		err := a.conversation.AddHistory(ctx, s.RunID, message)
		if err != nil {
			return nil, err
		}
		var callState types.ToolCallState
		for _, call := range s.Calls {
			if call.Call.ID == result.CallID {
				callState = call
				break
			}
		}
		err = a.event(ctx, s, "tool_end", result.CallID, callState)
		if err != nil {
			return nil, err
		}
	}
	s.Pending = nil
	return s, nil
}
