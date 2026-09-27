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
// A resumed blocked call gets a new entry so previous waiters keep their result.
type toolExecution struct {
	state  types.ToolCallState
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}
type toolExecutor struct {
	approvalResumed  bool // A node-scoped Eino answer authorizes only one call per resume.
	childCheckpoints map[string]*childCheckpointStore
	onStart          func(context.Context, types.ToolCallState) error
	beforeInvoke     func(context.Context, types.ToolCall) error
	eager            sync.WaitGroup
	registry         *tools.Registry
	runID            string
	parallelism      int
	policy           tools.Policy
	calls            map[string]*toolExecution
	mu               sync.Mutex
	eagerAllowed     map[string]string
	closed           bool
}

func newToolExecutor(runID string, registry *tools.Registry, parallelism int, policy tools.Policy) *toolExecutor {
	if parallelism < 1 {
		parallelism = 1
	}
	return &toolExecutor{
		registry: registry, runID: runID, parallelism: parallelism, policy: policy,
		calls:        make(map[string]*toolExecution),
		eagerAllowed: make(map[string]string),
	}
}
func (e *toolExecutor) key(callID string) string { return e.runID + "\x00" + callID }
func (e *toolExecutor) execute(ctx context.Context, call types.ToolCall, resume *types.ResumeAnswer, emit types.ToolChunkSink) (*types.ToolResult, error) {
	if call.ID == "" {
		return nil, fmt.Errorf("tool call ID is required")
	}
	key := e.key(call.ID)
	e.mu.Lock()
	entry := e.calls[key]
	if entry != nil {
		original := entry.state.Call
		if original.Name != call.Name || original.Arguments != call.Arguments {
			e.mu.Unlock()
			return nil, fmt.Errorf("tool call %s changed after execution started", call.ID)
		}
		switch entry.state.Status {
		case types.CallCompleted:
			result := copyToolResult(entry.state.Result)
			e.mu.Unlock()
			return result, nil
		case types.CallRunning:
			e.mu.Unlock()
			select {
			case <-entry.done:
				return copyToolResult(entry.state.Result), entry.err
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	if e.closed {
		e.mu.Unlock()
		return nil, context.Canceled
	}
	if entry != nil && entry.state.Status == types.CallOutcomeUnknown {
		e.mu.Unlock()
		return nil, fmt.Errorf("tool call %s has unknown outcome; explicit reconciliation required", call.ID)
	}
	runCtx, cancel := context.WithCancel(ctx)
	state := types.ToolCallState{Call: call, Status: types.CallRunning, StartedAt: time.Now()}
	entry = &toolExecution{state: state, done: make(chan struct{}), cancel: cancel}
	e.calls[key] = entry
	e.mu.Unlock()

	result, err := e.runTool(runCtx, state, resume, emit)
	cancel()
	e.mu.Lock()
	entry.state.Result = copyToolResult(result)
	entry.err = err
	entry.state.Status = types.CallCompleted
	if err != nil {
		_, interrupt := compose.IsInterruptRerunError(err)
		_, nested := compose.ExtractInterruptInfo(err)
		entry.state.Status = types.CallOutcomeUnknown
		if interrupt || nested {
			entry.state.Status = types.CallBlocked
		}
	}
	close(entry.done)
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

// runTool reports start and contains panics so the execution record always completes.
func (e *toolExecutor) runTool(ctx context.Context, state types.ToolCallState, resume *types.ResumeAnswer, emit types.ToolChunkSink) (result *types.ToolResult, err error) {
	call := state.Call
	// Tool code may panic after producing a side effect.
	// Return a system error, then finalize the shared ledger below so waiters
	// and cleanup cannot remain blocked on this execution forever.
	defer func() {
		recovered := recover()
		if recovered != nil {
			result = nil
			err = fmt.Errorf("tool %s panicked: %v", call.Name, recovered)
		}
	}()
	if e.onStart != nil {
		err = e.onStart(ctx, state)
		if err != nil {
			return nil, err
		}
	}
	return e.invoke(ctx, call, resume, emit)
}

func (e *toolExecutor) authorize(ctx context.Context, call types.ToolCall, resume *types.ResumeAnswer) (tools.Descriptor, *types.ToolResult, error) {
	d, ok := e.registry.Lookup(call.Name)
	result := &types.ToolResult{CallID: call.ID, ReturnDirect: d.ReturnDirect}
	if !ok {
		result.IsError = true
		result.Content = "unknown tool: " + call.Name
		return d, result, nil
	}
	err := ctx.Err()
	if err != nil {
		return d, nil, err
	}
	decision := tools.Decision{Action: tools.Allow}
	if d.RequiresApproval {
		decision.Action = tools.AskApproval
	}
	if e.policy != nil {
		e.mu.Lock()
		allowedArgs, eagerAllowed := e.eagerAllowed[e.key(call.ID)]
		e.mu.Unlock()
		if eagerAllowed {
			if allowedArgs != call.Arguments {
				return d, nil, fmt.Errorf("eager tool %s arguments changed after policy approval", call.ID)
			}
			decision.Action = tools.Allow
		} else {
			var err error
			decision, err = e.policy.Decide(ctx, call, d)
			if err != nil {
				return d, nil, err
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
		if resume == nil {
			target, hasData, answer := compose.GetResumeContext[*tools.ApprovalResult](ctx)
			e.mu.Lock()
			used := e.approvalResumed
			e.mu.Unlock()
			if target && hasData && answer != nil && !used {
				if answer.CallID != "" && answer.CallID != call.ID {
					return d, nil, fmt.Errorf("approval call ID %q does not match %q", answer.CallID, call.ID)
				}
				resume = &types.ResumeAnswer{CallID: call.ID, Approved: answer.Approved}
				e.mu.Lock()
				e.approvalResumed = true
				e.mu.Unlock()
			}
		}
		if resume == nil || resume.CallID != call.ID {
			info := &tools.ApprovalInfo{CallID: call.ID, ToolName: call.Name, Arguments: call.Arguments, ArgumentsInJSON: call.Arguments, Reason: decision.Reason}
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
			return d, nil, compose.Interrupt(ctx, info)
		}
		if resume.Approved {
			decision.Action = tools.Allow
		} else {
			decision.Action = tools.Deny
		}
	}
	if decision.Action == tools.Deny {
		result.IsError = true
		result.Content = decision.Reason
		if result.Content == "" {
			result.Content = "tool call denied"
		}
		return d, result, nil
	}
	if decision.Action != tools.Allow {
		return d, nil, fmt.Errorf("invalid policy action %q", decision.Action)
	}
	if !d.ReadOnly && e.beforeInvoke != nil {
		err = e.beforeInvoke(ctx, call)
		if err != nil {
			return d, nil, err
		}
	}
	return d, nil, nil
}
func (e *toolExecutor) executeBatch(ctx context.Context, calls []types.ToolCall, emit types.ToolChunkSink) ([]types.ToolResult, error) {
	ordered := append([]types.ToolCall(nil), calls...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Index < ordered[j].Index })
	results := make([]types.ToolResult, len(ordered))
	errs := make([]error, len(ordered))
	execute := func(i int) {
		result, err := e.execute(ctx, ordered[i], nil, emit)
		errs[i] = err
		if result != nil {
			results[i] = *result
		}
	}
	for start := 0; start < len(ordered); {
		d, _ := e.registry.Lookup(ordered[start].Name)
		if !d.ParallelSafe || d.RequiresApproval {
			execute(start)
			if errs[start] != nil {
				return nil, errs[start]
			}
			start++
			continue
		}
		end := start
		for end < len(ordered) {
			d, _ := e.registry.Lookup(ordered[end].Name)
			if !d.ParallelSafe || d.RequiresApproval {
				break
			}
			end++
		}
		var wg sync.WaitGroup
		semaphore := make(chan struct{}, e.parallelism)
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
				return nil, errs[i]
			}
			interrupts = append(interrupts, errs[i])
		}
		if len(interrupts) > 0 {
			return nil, compose.CompositeInterrupt(ctx, nil, nil, interrupts...)
		}
		start = end
	}
	return results, nil
}
func (e *toolExecutor) restore(calls []types.ToolCallState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, state := range calls {
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
		e.calls[e.key(state.Call.ID)] = &toolExecution{state: state}
	}
}

func (e *toolExecutor) cancel(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	var active []*toolExecution
	for _, entry := range e.calls {
		if entry.state.Status == types.CallRunning {
			entry.cancel()
			active = append(active, entry)
		}
	}
	e.mu.Unlock()
	for _, entry := range active {
		select {
		case <-entry.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.eager.Wait()
	return nil
}

// snapshot copies the ledger into the existing graph call order.
func (e *toolExecutor) snapshot(calls []types.ToolCallState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range calls {
		entry := e.calls[e.key(calls[i].Call.ID)]
		if entry == nil {
			calls[i].Status = types.CallPending
			continue
		}
		calls[i].Status = entry.state.Status
		calls[i].StartedAt = entry.state.StartedAt
		calls[i].Result = copyToolResult(entry.state.Result)
	}
}

func (e *toolExecutor) start(ctx context.Context, call types.ToolCall, emit types.ToolChunkSink) {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return
	}
	e.eager.Add(1)
	e.mu.Unlock()
	go func() { defer e.eager.Done(); _, _ = e.execute(ctx, call, nil, emit) }()
}

func (e *toolExecutor) startEagerIfAllowed(ctx context.Context, call types.ToolCall, emit types.ToolChunkSink) (bool, error) {
	d, ok := e.registry.Lookup(call.Name)
	if !ok || !d.ParallelSafe || d.RequiresApproval {
		return false, nil
	}
	if e.policy != nil {
		decision, err := e.policy.Decide(ctx, call, d)
		if err != nil {
			return false, err
		}
		if decision.Action != tools.Allow {
			return false, nil
		}
		e.mu.Lock()
		e.eagerAllowed[e.key(call.ID)] = call.Arguments
		e.mu.Unlock()
	}
	e.start(ctx, call, emit)
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
