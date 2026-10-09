package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"eino-cli/deepagent/graph/tools"
	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func (graph *Graph) callTools(ctx context.Context, runState *agentmodel.RunState) (*agentmodel.RunState, error) {
	runState.Phase = agentmodel.PhaseTools
	toolCalls := make([]agentmodel.ToolCall, len(runState.Calls))
	for i := range runState.Calls {
		toolCalls[i] = runState.Calls[i].Call
	}
	err := graph.toolExecutor.executeToolBatch(ctx, toolCalls, func(ctx context.Context, call agentmodel.ToolCall, chunk string) error {
		return graph.emitEvent(ctx, runState, "tool_call_output_chunk", call.ID, agentmodel.ToolCallOutputChunkPayload{Name: call.Name, CallID: call.ID, Chunk: chunk})
	})
	graph.toolExecutor.snapshotToolExecutions(runState.Calls)
	// A later call may interrupt this batch. Its first Eino snapshot must not
	// retain an approval obligation already resolved by a completed call.
	pending := runState.Pending[:0]
	for _, item := range runState.Pending {
		completed := false
		for _, call := range runState.Calls {
			if item.CallID != "" && item.CallID == call.Call.ID && call.Status == agentmodel.CallCompleted {
				completed = true
				break
			}
		}
		if !completed {
			pending = append(pending, item)
		}
	}
	runState.Pending = pending
	if err != nil {
		return nil, err
	}
	sort.SliceStable(runState.Calls, func(i, j int) bool { return runState.Calls[i].Call.Index < runState.Calls[j].Call.Index })
	for _, callState := range runState.Calls {
		call := callState.Call
		result := callState.Result
		if call.Name == tools.ToolUpdatePlan && !result.IsError {
			var update agentmodel.PlanUpdate
			err := json.Unmarshal([]byte(result.Content), &update)
			if err != nil {
				return nil, fmt.Errorf("decode update_plan result: %w", err)
			}
			err = graph.emitEvent(ctx, runState, "plan_updated", result.CallID, update)
			if err != nil {
				return nil, err
			}
		}
		message := agentmodel.NewToolMessage(result.Content, result.CallID)
		message.ToolName = call.Name
		if len(result.MultiContent) > 0 {
			message.Content = ""
			message.UserInputMultiContent = result.MultiContent
		}
		err := graph.conversation.AddHistory(ctx, runState.RunID, message)
		if err != nil {
			return nil, err
		}
		err = graph.emitEvent(ctx, runState, "tool_end", result.CallID, agentmodel.ToolEndPayload{MultiContent: result.MultiContent, Name: call.Name, CallID: call.ID, ArgumentsInJSON: call.Arguments, ToolStartTime: callState.StartedAt, Result: result.Content, IsError: result.IsError})
		if err != nil {
			return nil, err
		}
	}
	runState.Pending = nil
	return runState, nil
}

func (toolExecutor *toolExecutor) authorizeToolCall(ctx context.Context, toolCall agentmodel.ToolCall) (agentmodel.ToolDescriptor, *agentmodel.ToolResult, error) {
	toolDescriptor, ok := toolExecutor.toolSet.GetToolDescriptor(toolCall.Name)
	toolResult := &agentmodel.ToolResult{CallID: toolCall.ID, ReturnDirect: toolDescriptor.ReturnDirect}
	if !ok {
		toolResult.IsError = true
		toolResult.Content = "unknown tool: " + toolCall.Name
		return toolDescriptor, toolResult, nil
	}
	err := ctx.Err()
	if err != nil {
		return toolDescriptor, nil, err
	}
	decision := agentmodel.Decision{Action: agentmodel.Allow}
	if toolDescriptor.RequiresApproval {
		decision.Action = agentmodel.AskApproval
	}
	if toolExecutor.toolPolicy != nil {
		toolExecutor.mu.Lock()
		approvedArguments, hasEagerApproval := toolExecutor.approvedEagerArgumentsByCallID[toolCall.ID]
		toolExecutor.mu.Unlock()
		if hasEagerApproval {
			if approvedArguments != toolCall.Arguments {
				return toolDescriptor, nil, fmt.Errorf("eager tool %s arguments changed after policy approval", toolCall.ID)
			}
			decision.Action = agentmodel.Allow
		} else {
			var err error
			decision, err = toolExecutor.toolPolicy.Decide(ctx, toolCall, toolDescriptor)
			if err != nil {
				return toolDescriptor, nil, err
			}
		}
	}
	runState := agentmodel.GetRunState(ctx)
	if runState != nil && decision.Action == agentmodel.Allow {
		for _, pending := range runState.Pending {
			if pending.CallID == toolCall.ID && pending.Kind == "approval" {
				decision.Action = agentmodel.AskApproval
				break
			}
		}
	}
	if decision.Action == agentmodel.AskApproval {
		isResumeTarget, hasApprovalResult, approvalResult := compose.GetResumeContext[*agentmodel.ApprovalResult](ctx)
		toolExecutor.mu.Lock()
		canConsumeApprovalAnswer := isResumeTarget && hasApprovalResult && approvalResult != nil && !toolExecutor.approvalAnswerConsumed
		if canConsumeApprovalAnswer && approvalResult.CallID != "" && approvalResult.CallID != toolCall.ID {
			toolExecutor.mu.Unlock()
			return toolDescriptor, nil, fmt.Errorf("approval call ID %q does not match %q", approvalResult.CallID, toolCall.ID)
		}
		if canConsumeApprovalAnswer {
			toolExecutor.approvalAnswerConsumed = true
		}
		toolExecutor.mu.Unlock()
		if !canConsumeApprovalAnswer {
			approvalInfo := &agentmodel.ApprovalInfo{CallID: toolCall.ID, ToolName: toolCall.Name, Arguments: toolCall.Arguments, Reason: decision.Reason}
			// Approval calls are sequential barriers. Persist the obligation before
			// Eino saves its first snapshot, even if later ID enrichment fails.
			if runState != nil {
				found := false
				for _, pending := range runState.Pending {
					if pending.CallID == toolCall.ID && pending.Kind == "approval" {
						found = true
					}
				}
				if !found {
					data, _ := json.Marshal(approvalInfo)
					runState.Pending = append(runState.Pending, agentmodel.Interrupt{CallID: toolCall.ID, Kind: "approval", Data: data})
				}
			}
			return toolDescriptor, nil, compose.Interrupt(ctx, approvalInfo)
		}
		if approvalResult.Approved {
			decision.Action = agentmodel.Allow
		} else {
			decision.Action = agentmodel.Deny
			if approvalResult.DisapproveReason != nil && *approvalResult.DisapproveReason != "" {
				decision.Reason = *approvalResult.DisapproveReason
			}
		}
	}
	if decision.Action == agentmodel.Deny {
		toolResult.IsError = true
		toolResult.Content = decision.Reason
		if toolResult.Content == "" {
			toolResult.Content = "tool call denied"
		}
		return toolDescriptor, toolResult, nil
	}
	if decision.Action != agentmodel.Allow {
		return toolDescriptor, nil, fmt.Errorf("invalid policy action %q", decision.Action)
	}
	if !toolDescriptor.ReadOnly && toolExecutor.persistToolExecutionFence != nil {
		err = toolExecutor.persistToolExecutionFence(ctx, toolCall)
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
func (toolExecutor *toolExecutor) invokeTool(ctx context.Context, toolCallState agentmodel.ToolCallState, emitToolChunk agentmodel.ToolChunkSink) (toolResult *agentmodel.ToolResult, err error) {
	toolCall := toolCallState.Call
	// Tool code may panic after producing a side effect.
	// Return a system error so execute can finalize the shared ledger and waiters
	// and cleanup cannot remain blocked on this execution forever.
	defer func() {
		recovered := recover()
		if recovered != nil {
			toolResult = nil
			err = fmt.Errorf("tool %s panicked: %v", toolCall.Name, recovered)
		}
	}()
	if toolExecutor.onToolStart != nil {
		err = toolExecutor.onToolStart(ctx, toolCallState)
		if err != nil {
			return nil, err
		}
	}

	ctx = context.WithValue(ctx, toolCallIDKey{}, toolCall.ID)
	ctx = context.WithValue(ctx, toolExecutorKey{}, toolExecutor)
	toolDescriptor, policyResult, err := toolExecutor.authorizeToolCall(ctx, toolCall)
	if policyResult != nil || err != nil {
		return policyResult, err
	}
	toolResult = &agentmodel.ToolResult{CallID: toolCall.ID, ReturnDirect: toolDescriptor.ReturnDirect}
	switch einoTool := toolDescriptor.Tool.(type) {
	case tool.EnhancedStreamableTool:
		stream, err := einoTool.StreamableRun(ctx, &schema.ToolArgument{Text: toolCall.Arguments})
		if stream != nil {
			defer stream.Close()
		}
		if err != nil {
			return finishToolResult(ctx, toolResult, err)
		}
		if stream == nil {
			return nil, fmt.Errorf("tool %s returned nil stream", toolCall.Name)
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
				return finishToolResult(ctx, toolResult, readErr)
			}
			if chunk == nil {
				continue
			}
			converted, convertErr := finishEnhancedResult(&agentmodel.ToolResult{}, chunk)
			if convertErr != nil {
				return nil, convertErr
			}
			chunks = append(chunks, chunk)
			if emitToolChunk != nil && converted.Content != "" {
				err = emitToolChunk(ctx, toolCall, converted.Content)
				if err != nil {
					return nil, err
				}
			}
		}
		merged, err := schema.ConcatToolResults(chunks)
		if err != nil {
			return nil, err
		}
		return finishEnhancedResult(toolResult, merged)
	case tool.EnhancedInvokableTool:
		einoToolResult, err := einoTool.InvokableRun(ctx, &schema.ToolArgument{Text: toolCall.Arguments})
		if err != nil {
			return finishToolResult(ctx, toolResult, err)
		}
		return finishEnhancedResult(toolResult, einoToolResult)
	case tool.StreamableTool:
		stream, err := einoTool.StreamableRun(ctx, toolCall.Arguments)
		if stream != nil {
			defer stream.Close()
		}
		if err != nil {
			return finishToolResult(ctx, toolResult, err)
		}
		if stream == nil {
			return nil, fmt.Errorf("tool %s returned nil stream", toolCall.Name)
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
				return finishToolResult(ctx, toolResult, readErr)
			}
			toolResult.Content += chunk
			if emitToolChunk != nil {
				err = emitToolChunk(ctx, toolCall, chunk)
				if err != nil {
					return nil, err
				}
			}
		}
		return toolResult, nil
	case tool.InvokableTool:
		toolResult.Content, err = einoTool.InvokableRun(ctx, toolCall.Arguments)
		return finishToolResult(ctx, toolResult, err)
	default:
		return nil, fmt.Errorf("tool %s has no Eino execution interface", toolCall.Name)
	}
}

func finishToolResult(ctx context.Context, toolResult *agentmodel.ToolResult, err error) (*agentmodel.ToolResult, error) {
	if err == nil {
		return toolResult, nil
	}
	var internalError *agentmodel.InternalError
	if errors.As(err, &internalError) {
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
	toolResult.IsError = true
	toolResult.Content = err.Error()
	return toolResult, nil
}

func finishEnhancedResult(toolResult *agentmodel.ToolResult, einoToolResult *schema.ToolResult) (*agentmodel.ToolResult, error) {
	if einoToolResult == nil {
		return nil, fmt.Errorf("enhanced tool returned nil output")
	}
	parts, err := einoToolResult.ToMessageInputParts()
	if err != nil {
		return nil, err
	}
	toolResult.MultiContent = parts
	toolResult.Content = ""
	for _, part := range parts {
		if part.Type == schema.ChatMessagePartTypeText {
			toolResult.Content += part.Text
		}
	}
	return toolResult, nil
}
