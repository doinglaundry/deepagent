package execution

import (
	"context"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"io"
	"sort"
)

func (a *Graph) callTools(ctx context.Context, s *types.RunState) (*types.RunState, error) {
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

func (e *toolExecutor) authorize(ctx context.Context, call types.ToolCall) (tools.ToolDescriptor, *types.ToolResult, error) {
	toolDescriptor, ok := e.toolset.Lookup(call.Name)
	result := &types.ToolResult{CallID: call.ID, ReturnDirect: toolDescriptor.ReturnDirect}
	if !ok {
		result.IsError = true
		result.Content = "unknown tool: " + call.Name
		return toolDescriptor, result, nil
	}
	err := ctx.Err()
	if err != nil {
		return toolDescriptor, nil, err
	}
	decision := tools.Decision{Action: tools.Allow}
	if toolDescriptor.RequiresApproval {
		decision.Action = tools.AskApproval
	}
	if e.toolPolicy != nil {
		e.mu.Lock()
		approvedArguments, hasEagerApproval := e.approvedEagerArgumentsByCallID[call.ID]
		e.mu.Unlock()
		if hasEagerApproval {
			if approvedArguments != call.Arguments {
				return toolDescriptor, nil, fmt.Errorf("eager tool %s arguments changed after policy approval", call.ID)
			}
			decision.Action = tools.Allow
		} else {
			var err error
			decision, err = e.toolPolicy.Decide(ctx, call, toolDescriptor)
			if err != nil {
				return toolDescriptor, nil, err
			}
		}
	}
	state := types.RunStateFromContext(ctx)
	if state != nil && decision.Action == tools.Allow {
		for _, pending := range state.Pending {
			if pending.CallID == call.ID && pending.Kind == "approval" {
				decision.Action = tools.AskApproval
				break
			}
		}
	}
	if decision.Action == tools.AskApproval {
		target, hasData, answer := compose.GetResumeContext[*tools.ApprovalResult](ctx)
		e.mu.Lock()
		available := target && hasData && answer != nil && !e.approvalAnswerConsumed
		if available && answer.CallID != "" && answer.CallID != call.ID {
			e.mu.Unlock()
			return toolDescriptor, nil, fmt.Errorf("approval call ID %q does not match %q", answer.CallID, call.ID)
		}
		if available {
			e.approvalAnswerConsumed = true
		}
		e.mu.Unlock()
		if !available {
			info := &tools.ApprovalInfo{CallID: call.ID, ToolName: call.Name, Arguments: call.Arguments, Reason: decision.Reason}
			// Approval calls are sequential barriers. Persist the obligation before
			// Eino saves its first snapshot, even if later ID enrichment fails.
			if state != nil {
				found := false
				for _, pending := range state.Pending {
					if pending.CallID == call.ID && pending.Kind == "approval" {
						found = true
					}
				}
				if !found {
					data, _ := json.Marshal(info)
					state.Pending = append(state.Pending, types.Interrupt{CallID: call.ID, Kind: "approval", Data: data})
				}
			}
			return toolDescriptor, nil, compose.Interrupt(ctx, info)
		}
		if answer.Approved {
			decision.Action = tools.Allow
		} else {
			decision.Action = tools.Deny
			if answer.DisapproveReason != nil && *answer.DisapproveReason != "" {
				decision.Reason = *answer.DisapproveReason
			}
		}
	}
	if decision.Action == tools.Deny {
		result.IsError = true
		result.Content = decision.Reason
		if result.Content == "" {
			result.Content = "tool call denied"
		}
		return toolDescriptor, result, nil
	}
	if decision.Action != tools.Allow {
		return toolDescriptor, nil, fmt.Errorf("invalid policy action %q", decision.Action)
	}
	if !toolDescriptor.ReadOnly && e.persistToolExecutionFence != nil {
		err = e.persistToolExecutionFence(ctx, call)
		if err != nil {
			return toolDescriptor, nil, err
		}
	}
	return toolDescriptor, nil, nil
}

// GetToolCallID returns the identity assigned by the current tool execution.
func GetToolCallID(ctx context.Context) string {
	id, ok := ctx.Value(toolCallIDKey{}).(string)
	if ok && id != "" {
		return id
	}
	return compose.GetToolCallID(ctx)
}

// Every tool passes policy and checkpoint fencing before interface dispatch.
func (e *toolExecutor) invokeTool(ctx context.Context, state types.ToolCallState, emit types.ToolChunkSink) (result *types.ToolResult, err error) {
	call := state.Call
	// Tool code may panic after producing a side effect.
	// Return a system error so execute can finalize the shared ledger and waiters
	// and cleanup cannot remain blocked on this execution forever.
	defer func() {
		recovered := recover()
		if recovered != nil {
			result = nil
			err = fmt.Errorf("tool %s panicked: %v", call.Name, recovered)
		}
	}()
	if e.onToolStart != nil {
		err = e.onToolStart(ctx, state)
		if err != nil {
			return nil, err
		}
	}

	ctx = context.WithValue(ctx, toolCallIDKey{}, call.ID)
	ctx = context.WithValue(ctx, toolExecutorKey{}, e)
	descriptor, early, err := e.authorize(ctx, call)
	if early != nil || err != nil {
		return early, err
	}
	result = &types.ToolResult{CallID: call.ID, ReturnDirect: descriptor.ReturnDirect}
	switch t := descriptor.Tool.(type) {
	case tool.EnhancedStreamableTool:
		stream, err := t.StreamableRun(ctx, &schema.ToolArgument{Text: call.Arguments})
		if stream != nil {
			defer stream.Close()
		}
		if err != nil {
			return finishToolResult(ctx, result, err)
		}
		if stream == nil {
			return nil, fmt.Errorf("tool %s returned nil stream", call.Name)
		}
		var chunks []*schema.ToolResult
		for {
			err = ctx.Err()
			if err != nil {
				return nil, err
			}
			chunk, readErr := stream.Recv()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return finishToolResult(ctx, result, readErr)
			}
			if chunk == nil {
				continue
			}
			converted, convertErr := finishEnhancedResult(&types.ToolResult{}, chunk)
			if convertErr != nil {
				return nil, convertErr
			}
			chunks = append(chunks, chunk)
			if emit != nil && converted.Content != "" {
				err = emit(ctx, call, converted.Content)
				if err != nil {
					return nil, err
				}
			}
		}
		merged, err := schema.ConcatToolResults(chunks)
		if err != nil {
			return nil, err
		}
		return finishEnhancedResult(result, merged)
	case tool.EnhancedInvokableTool:
		output, err := t.InvokableRun(ctx, &schema.ToolArgument{Text: call.Arguments})
		if err != nil {
			return finishToolResult(ctx, result, err)
		}
		return finishEnhancedResult(result, output)
	case tool.StreamableTool:
		stream, err := t.StreamableRun(ctx, call.Arguments)
		if stream != nil {
			defer stream.Close()
		}
		if err != nil {
			return finishToolResult(ctx, result, err)
		}
		if stream == nil {
			return nil, fmt.Errorf("tool %s returned nil stream", call.Name)
		}
		for {
			err = ctx.Err()
			if err != nil {
				return nil, err
			}
			chunk, readErr := stream.Recv()
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return finishToolResult(ctx, result, readErr)
			}
			result.Content += chunk
			if emit != nil {
				err = emit(ctx, call, chunk)
				if err != nil {
					return nil, err
				}
			}
		}
		return result, nil
	case tool.InvokableTool:
		result.Content, err = t.InvokableRun(ctx, call.Arguments)
		return finishToolResult(ctx, result, err)
	default:
		return nil, fmt.Errorf("tool %s has no Eino execution interface", call.Name)
	}
}

func finishToolResult(ctx context.Context, result *types.ToolResult, err error) (*types.ToolResult, error) {
	if err == nil {
		return result, nil
	}
	var internal *types.InternalError
	if errors.As(err, &internal) {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	_, interrupted := compose.ExtractInterruptInfo(err)
	_, rerun := compose.IsInterruptRerunError(err)
	if interrupted || rerun || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	result.IsError = true
	result.Content = err.Error()
	return result, nil
}

func finishEnhancedResult(result *types.ToolResult, output *schema.ToolResult) (*types.ToolResult, error) {
	if output == nil {
		return nil, fmt.Errorf("enhanced tool returned nil output")
	}
	parts, err := output.ToMessageInputParts()
	if err != nil {
		return nil, err
	}
	result.MultiContent = parts
	result.Content = ""
	for _, part := range parts {
		if part.Type == schema.ChatMessagePartTypeText {
			result.Content += part.Text
		}
	}
	return result, nil
}
