package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

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
	err := a.executor.executeBatch(ctx, calls, func(ctx context.Context, call types.ToolCall, chunk string) error {
		return a.event(ctx, s, "tool_call_output_chunk", call.ID, types.ToolCallOutputChunkPayload{Name: call.Name, CallID: call.ID, Chunk: chunk})
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
	sort.SliceStable(s.Calls, func(i, j int) bool { return s.Calls[i].Call.Index < s.Calls[j].Call.Index })
	for _, callState := range s.Calls {
		call := callState.Call
		result := callState.Result
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
		err = a.event(ctx, s, "tool_end", result.CallID, types.ToolEndPayload{MultiContent: result.MultiContent, Name: call.Name, CallID: call.ID, ArgumentsInJSON: call.Arguments, ToolStartTime: callState.StartedAt, Result: result.Content})
		if err != nil {
			return nil, err
		}
	}
	s.Pending = nil
	return s, nil
}
