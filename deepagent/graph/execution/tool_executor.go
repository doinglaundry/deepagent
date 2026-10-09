package execution

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"eino-cli/deepagent/graph/tools"
	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/compose"
)

// toolExecution owns one attempt and its durable call state.
// A resumed blocked call gets a new execution record so previous waiters keep their result.
type toolExecution struct {
	toolCallState agentmodel.ToolCallState
	done          chan struct{}
	cancel        context.CancelFunc
	err           error
}

type toolExecutor struct {
	approvalAnswerConsumed         bool // A node-scoped Eino answer authorizes only one tool call per resume.
	childCheckpointStoresByCallID  map[string]*childCheckpointStore
	onToolStart                    func(context.Context, agentmodel.ToolCallState) error
	persistToolExecutionFence      func(context.Context, agentmodel.ToolCall) error
	eagerToolWaitGroup             sync.WaitGroup
	toolSet                        *tools.ToolSet
	maxParallelTools               int
	toolPolicy                     agentmodel.Policy
	toolExecutionsByCallID         map[string]*toolExecution
	mu                             sync.Mutex
	approvedEagerArgumentsByCallID map[string]string
	isClosed                       bool
}

func newToolExecutor(toolSet *tools.ToolSet, maxParallelTools int, toolPolicy agentmodel.Policy) *toolExecutor {
	if maxParallelTools < 1 {
		maxParallelTools = 1
	}
	return &toolExecutor{
		toolSet: toolSet, maxParallelTools: maxParallelTools, toolPolicy: toolPolicy,
		toolExecutionsByCallID:         make(map[string]*toolExecution),
		approvedEagerArgumentsByCallID: make(map[string]string),
	}
}

func (toolExecutor *toolExecutor) executeToolCall(ctx context.Context, toolCall agentmodel.ToolCall, emitToolChunk agentmodel.ToolChunkSink) (*agentmodel.ToolResult, error) {
	if toolCall.ID == "" {
		return nil, fmt.Errorf("tool call ID is required")
	}
	callID := toolCall.ID
	toolExecutor.mu.Lock()
	toolExecutionRecord := toolExecutor.toolExecutionsByCallID[callID]
	if toolExecutionRecord != nil {
		originalToolCall := toolExecutionRecord.toolCallState.Call
		if originalToolCall.Name != toolCall.Name || originalToolCall.Arguments != toolCall.Arguments {
			toolExecutor.mu.Unlock()
			return nil, fmt.Errorf("tool call %s changed after execution started", toolCall.ID)
		}
		switch toolExecutionRecord.toolCallState.Status {
		case agentmodel.CallCompleted:
			toolResult := copyToolResult(toolExecutionRecord.toolCallState.Result)
			toolExecutor.mu.Unlock()
			return toolResult, nil
		case agentmodel.CallRunning:
			toolExecutor.mu.Unlock()
			select {
			case <-toolExecutionRecord.done:
				return copyToolResult(toolExecutionRecord.toolCallState.Result), toolExecutionRecord.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	if toolExecutor.isClosed {
		toolExecutor.mu.Unlock()
		return nil, context.Canceled
	}
	if toolExecutionRecord != nil && toolExecutionRecord.toolCallState.Status == agentmodel.CallOutcomeUnknown {
		toolExecutor.mu.Unlock()
		return nil, fmt.Errorf("tool call %s has unknown outcome; explicit reconciliation required", toolCall.ID)
	}
	toolCtx, cancel := context.WithCancel(ctx)
	toolCallState := agentmodel.ToolCallState{Call: toolCall, Status: agentmodel.CallRunning, StartedAt: time.Now()}
	toolExecutionRecord = &toolExecution{toolCallState: toolCallState, done: make(chan struct{}), cancel: cancel}
	toolExecutor.toolExecutionsByCallID[callID] = toolExecutionRecord
	toolExecutor.mu.Unlock()

	toolResult, err := toolExecutor.invokeTool(toolCtx, toolCallState, emitToolChunk)
	cancel()
	toolExecutor.mu.Lock()
	toolExecutionRecord.toolCallState.Result = copyToolResult(toolResult)
	toolExecutionRecord.err = err
	toolExecutionRecord.toolCallState.Status = agentmodel.CallCompleted
	if err != nil {
		_, isToolInterrupted := compose.IsInterruptRerunError(err)
		_, hasNestedInterrupt := compose.ExtractInterruptInfo(err)
		toolExecutionRecord.toolCallState.Status = agentmodel.CallOutcomeUnknown
		if isToolInterrupted || hasNestedInterrupt {
			toolExecutionRecord.toolCallState.Status = agentmodel.CallBlocked
		}
	}
	close(toolExecutionRecord.done)
	toolExecutor.mu.Unlock()
	return toolResult, err
}

func copyToolResult(toolResult *agentmodel.ToolResult) *agentmodel.ToolResult {
	if toolResult == nil {
		return nil
	}
	resultCopy := *toolResult
	return &resultCopy
}

func (toolExecutor *toolExecutor) executeToolBatch(ctx context.Context, toolCalls []agentmodel.ToolCall, emitToolChunk agentmodel.ToolChunkSink) error {
	orderedToolCalls := append([]agentmodel.ToolCall(nil), toolCalls...)
	sort.SliceStable(orderedToolCalls, func(i, j int) bool { return orderedToolCalls[i].Index < orderedToolCalls[j].Index })
	executionErrors := make([]error, len(orderedToolCalls))
	executeCall := func(i int) {
		_, err := toolExecutor.executeToolCall(ctx, orderedToolCalls[i], emitToolChunk)
		executionErrors[i] = err
	}
	for batchStart := 0; batchStart < len(orderedToolCalls); {
		toolDescriptor, _ := toolExecutor.toolSet.GetToolDescriptor(orderedToolCalls[batchStart].Name)
		if !toolDescriptor.ParallelSafe || toolDescriptor.RequiresApproval {
			executeCall(batchStart)
			if executionErrors[batchStart] != nil {
				return executionErrors[batchStart]
			}
			batchStart++
			continue
		}
		batchEnd := batchStart
		for batchEnd < len(orderedToolCalls) {
			toolDescriptor, _ := toolExecutor.toolSet.GetToolDescriptor(orderedToolCalls[batchEnd].Name)
			if !toolDescriptor.ParallelSafe || toolDescriptor.RequiresApproval {
				break
			}
			batchEnd++
		}
		var toolWaitGroup sync.WaitGroup
		semaphore := make(chan struct{}, toolExecutor.maxParallelTools)
		for i := batchStart; i < batchEnd; i++ {
			semaphore <- struct{}{}
			toolWaitGroup.Add(1)
			go func(index int) { defer toolWaitGroup.Done(); defer func() { <-semaphore }(); executeCall(index) }(i)
		}
		toolWaitGroup.Wait()
		var interruptErrors []error
		for i := batchStart; i < batchEnd; i++ {
			if executionErrors[i] == nil {
				continue
			}
			_, rerun := compose.IsInterruptRerunError(executionErrors[i])
			_, interrupted := compose.ExtractInterruptInfo(executionErrors[i])
			if !rerun && !interrupted {
				return executionErrors[i]
			}
			interruptErrors = append(interruptErrors, executionErrors[i])
		}
		if len(interruptErrors) > 0 {
			return compose.CompositeInterrupt(ctx, nil, nil, interruptErrors...)
		}
		batchStart = batchEnd
	}
	return nil
}

func (toolExecutor *toolExecutor) restoreToolExecutions(toolCalls []agentmodel.ToolCallState) {
	toolExecutor.mu.Lock()
	defer toolExecutor.mu.Unlock()
	for _, toolCallState := range toolCalls {
		toolCallState.Result = copyToolResult(toolCallState.Result)
		switch toolCallState.Status {
		case agentmodel.CallRunning:
			// The process that owned this invocation is gone; do not replay it.
			toolCallState.Status = agentmodel.CallOutcomeUnknown
		case agentmodel.CallCompleted:
			if toolCallState.Result == nil {
				toolCallState.Status = agentmodel.CallPending
			}
		}
		toolExecutor.toolExecutionsByCallID[toolCallState.Call.ID] = &toolExecution{toolCallState: toolCallState}
	}
}

func (toolExecutor *toolExecutor) cancelToolExecutions(ctx context.Context) error {
	toolExecutor.mu.Lock()
	toolExecutor.isClosed = true
	var activeToolExecutions []*toolExecution
	for _, toolExecutionRecord := range toolExecutor.toolExecutionsByCallID {
		if toolExecutionRecord.toolCallState.Status == agentmodel.CallRunning {
			toolExecutionRecord.cancel()
			activeToolExecutions = append(activeToolExecutions, toolExecutionRecord)
		}
	}
	toolExecutor.mu.Unlock()
	for _, toolExecutionRecord := range activeToolExecutions {
		select {
		case <-toolExecutionRecord.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	toolExecutor.eagerToolWaitGroup.Wait()
	return nil
}

// snapshotToolExecutions copies the ledger into the existing graph call order.
func (toolExecutor *toolExecutor) snapshotToolExecutions(toolCalls []agentmodel.ToolCallState) {
	toolExecutor.mu.Lock()
	defer toolExecutor.mu.Unlock()
	for i := range toolCalls {
		toolExecutionRecord := toolExecutor.toolExecutionsByCallID[toolCalls[i].Call.ID]
		if toolExecutionRecord == nil {
			toolCalls[i].Status = agentmodel.CallPending
			continue
		}
		toolCalls[i].Status = toolExecutionRecord.toolCallState.Status
		toolCalls[i].StartedAt = toolExecutionRecord.toolCallState.StartedAt
		toolCalls[i].Result = copyToolResult(toolExecutionRecord.toolCallState.Result)
	}
}

func (toolExecutor *toolExecutor) startEagerToolIfAllowed(ctx context.Context, toolCall agentmodel.ToolCall, emitToolChunk agentmodel.ToolChunkSink) (bool, error) {
	toolDescriptor, ok := toolExecutor.toolSet.GetToolDescriptor(toolCall.Name)
	if !ok || !toolDescriptor.ParallelSafe || toolDescriptor.RequiresApproval {
		return false, nil
	}
	if toolExecutor.toolPolicy != nil {
		policyDecision, err := toolExecutor.toolPolicy.Decide(ctx, toolCall, toolDescriptor)
		if err != nil {
			return false, err
		}
		if policyDecision.Action != agentmodel.Allow {
			return false, nil
		}
		toolExecutor.mu.Lock()
		toolExecutor.approvedEagerArgumentsByCallID[toolCall.ID] = toolCall.Arguments
		toolExecutor.mu.Unlock()
	}
	toolExecutor.mu.Lock()
	if toolExecutor.isClosed {
		toolExecutor.mu.Unlock()
		return false, nil
	}
	toolExecutor.eagerToolWaitGroup.Add(1)
	toolExecutor.mu.Unlock()
	go func() {
		defer toolExecutor.eagerToolWaitGroup.Done()
		_, _ = toolExecutor.executeToolCall(ctx, toolCall, emitToolChunk)
	}()
	return true, nil
}
