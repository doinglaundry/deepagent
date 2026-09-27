package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"

	"github.com/cloudwego/eino/compose"
)

// toolExecution owns one attempt and its durable call state.
// A resumed blocked call gets a new execution record so previous waiters keep their result.
type toolExecution struct {
	state  types.ToolCallState
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}
type toolExecutor struct {
	approvalAnswerConsumed        bool // A node-scoped Eino answer authorizes only one tool call per resume.
	childCheckpointStoresByCallID map[string]*childCheckpointStore
	onToolStart                   func(context.Context, types.ToolCallState) error
	persistToolExecutionFence     func(context.Context, types.ToolCall) error
	eagerToolWaitGroup            sync.WaitGroup
	toolset                       *tools.ToolSet
	runID                         string
	maxParallelTools              int
	toolPolicy                    tools.Policy
	toolExecutionsByKey           map[string]*toolExecution
	mu                            sync.Mutex
	approvedEagerArgumentsByKey   map[string]string
	isClosed                      bool
}

func newToolExecutor(runID string, toolSet *tools.ToolSet, maxParallelTools int, toolPolicy tools.Policy) *toolExecutor {
	if maxParallelTools < 1 {
		maxParallelTools = 1
	}
	return &toolExecutor{
		toolset: toolSet, runID: runID, maxParallelTools: maxParallelTools, toolPolicy: toolPolicy,
		toolExecutionsByKey:         make(map[string]*toolExecution),
		approvedEagerArgumentsByKey: make(map[string]string),
	}
}

// executionKey combines RunID and CallID; both keyed maps use this identity.
func (e *toolExecutor) executionKey(callID string) string { return e.runID + "\x00" + callID }
func (e *toolExecutor) execute(ctx context.Context, call types.ToolCall, emit types.ToolChunkSink) (*types.ToolResult, error) {
	if call.ID == "" {
		return nil, fmt.Errorf("tool call ID is required")
	}
	executionKey := e.executionKey(call.ID)
	e.mu.Lock()
	execution := e.toolExecutionsByKey[executionKey]
	if execution != nil {
		original := execution.state.Call
		if original.Name != call.Name || original.Arguments != call.Arguments {
			e.mu.Unlock()
			return nil, fmt.Errorf("tool call %s changed after execution started", call.ID)
		}
		switch execution.state.Status {
		case types.CallCompleted:
			result := copyToolResult(execution.state.Result)
			e.mu.Unlock()
			return result, nil
		case types.CallRunning:
			e.mu.Unlock()
			select {
			case <-execution.done:
				return copyToolResult(execution.state.Result), execution.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	if e.isClosed {
		e.mu.Unlock()
		return nil, context.Canceled
	}
	if execution != nil && execution.state.Status == types.CallOutcomeUnknown {
		e.mu.Unlock()
		return nil, fmt.Errorf("tool call %s has unknown outcome; explicit reconciliation required", call.ID)
	}
	runCtx, cancel := context.WithCancel(ctx)
	state := types.ToolCallState{Call: call, Status: types.CallRunning, StartedAt: time.Now()}
	execution = &toolExecution{state: state, done: make(chan struct{}), cancel: cancel}
	e.toolExecutionsByKey[executionKey] = execution
	e.mu.Unlock()

	result, err := e.invokeTool(runCtx, state, emit)
	cancel()
	e.mu.Lock()
	execution.state.Result = copyToolResult(result)
	execution.err = err
	execution.state.Status = types.CallCompleted
	if err != nil {
		_, interrupt := compose.IsInterruptRerunError(err)
		_, nested := compose.ExtractInterruptInfo(err)
		execution.state.Status = types.CallOutcomeUnknown
		if interrupt || nested {
			execution.state.Status = types.CallBlocked
		}
	}
	close(execution.done)
	e.mu.Unlock()
	return result, err
}

func copyToolResult(result *types.ToolResult) *types.ToolResult {
	if result == nil {
		return nil
	}
	copy := *result
	return &copy
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
		approvedArguments, hasEagerApproval := e.approvedEagerArgumentsByKey[e.executionKey(call.ID)]
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
func (e *toolExecutor) executeBatch(ctx context.Context, toolCalls []types.ToolCall, emit types.ToolChunkSink) error {
	ordered := append([]types.ToolCall(nil), toolCalls...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })
	errs := make([]error, len(ordered))
	execute := func(i int) {
		_, err := e.execute(ctx, ordered[i], emit)
		errs[i] = err
	}
	for start := 0; start < len(ordered); {
		toolDescriptor, _ := e.toolset.Lookup(ordered[start].Name)
		if !toolDescriptor.ParallelSafe || toolDescriptor.RequiresApproval {
			execute(start)
			if errs[start] != nil {
				return errs[start]
			}
			start++
			continue
		}
		end := start
		for end < len(ordered) {
			toolDescriptor, _ := e.toolset.Lookup(ordered[end].Name)
			if !toolDescriptor.ParallelSafe || toolDescriptor.RequiresApproval {
				break
			}
			end++
		}
		var wg sync.WaitGroup
		semaphore := make(chan struct{}, e.maxParallelTools)
		for i := start; i < end; i++ {
			semaphore <- struct{}{}
			wg.Add(1)
			go func(index int) { defer wg.Done(); defer func() { <-semaphore }(); execute(index) }(i)
		}
		wg.Wait()
		var interrupts []error
		for i := start; i < end; i++ {
			if errs[i] == nil {
				continue
			}
			_, rerun := compose.IsInterruptRerunError(errs[i])
			_, interrupted := compose.ExtractInterruptInfo(errs[i])
			if !rerun && !interrupted {
				return errs[i]
			}
			interrupts = append(interrupts, errs[i])
		}
		if len(interrupts) > 0 {
			return compose.CompositeInterrupt(ctx, nil, nil, interrupts...)
		}
		start = end
	}
	return nil
}
func (e *toolExecutor) restore(toolCalls []types.ToolCallState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, state := range toolCalls {
		state.Result = copyToolResult(state.Result)
		switch state.Status {
		case types.CallRunning:
			// The process that owned this invocation is gone; do not replay it.
			state.Status = types.CallOutcomeUnknown
		case types.CallCompleted:
			if state.Result == nil {
				state.Status = types.CallPending
			}
		}
		e.toolExecutionsByKey[e.executionKey(state.Call.ID)] = &toolExecution{state: state}
	}
}

func (e *toolExecutor) cancel(ctx context.Context) error {
	e.mu.Lock()
	e.isClosed = true
	var active []*toolExecution
	for _, toolExecutionRecord := range e.toolExecutionsByKey {
		if toolExecutionRecord.state.Status == types.CallRunning {
			toolExecutionRecord.cancel()
			active = append(active, toolExecutionRecord)
		}
	}
	e.mu.Unlock()
	for _, toolExecutionRecord := range active {
		select {
		case <-toolExecutionRecord.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.eagerToolWaitGroup.Wait()
	return nil
}

// snapshot copies the ledger into the existing graph call order.
func (e *toolExecutor) snapshot(toolCalls []types.ToolCallState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range toolCalls {
		execution := e.toolExecutionsByKey[e.executionKey(toolCalls[i].Call.ID)]
		if execution == nil {
			toolCalls[i].Status = types.CallPending
			continue
		}
		toolCalls[i].Status = execution.state.Status
		toolCalls[i].StartedAt = execution.state.StartedAt
		toolCalls[i].Result = copyToolResult(execution.state.Result)
	}
}

func (e *toolExecutor) startEagerIfAllowed(ctx context.Context, call types.ToolCall, emit types.ToolChunkSink) (bool, error) {
	toolDescriptor, ok := e.toolset.Lookup(call.Name)
	if !ok || !toolDescriptor.ParallelSafe || toolDescriptor.RequiresApproval {
		return false, nil
	}
	if e.toolPolicy != nil {
		decision, err := e.toolPolicy.Decide(ctx, call, toolDescriptor)
		if err != nil {
			return false, err
		}
		if decision.Action != tools.Allow {
			return false, nil
		}
		e.mu.Lock()
		e.approvedEagerArgumentsByKey[e.executionKey(call.ID)] = call.Arguments
		e.mu.Unlock()
	}
	e.mu.Lock()
	if e.isClosed {
		e.mu.Unlock()
		return false, nil
	}
	e.eagerToolWaitGroup.Add(1)
	e.mu.Unlock()
	go func() {
		defer e.eagerToolWaitGroup.Done()
		_, _ = e.execute(ctx, call, emit)
	}()
	return true, nil
}

// GetToolCallID returns the identity assigned by the current tool execution.
func GetToolCallID(ctx context.Context) string {
	id, ok := ctx.Value(toolCallIDKey{}).(string)
	if ok && id != "" {
		return id
	}
	return compose.GetToolCallID(ctx)
}
