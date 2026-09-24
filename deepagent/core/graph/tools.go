package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
)

type runningTool struct {
	done   chan struct{}
	cancel context.CancelFunc
	result *types.ToolResult
	err    error
	call   types.ToolCall
}
type toolExecutor struct {
	approvalResumed  bool // A node-scoped Eino answer authorizes only one call per resume.
	childCheckpoints map[string]*childCheckpointStore
	identities       map[string]types.ToolCall
	onStart          func(context.Context, types.ToolCallState) error
	beforeInvoke     func(context.Context, types.ToolCall) error
	started          map[string]time.Time
	middlewares      []middleware.Middleware
	eager            sync.WaitGroup
	registry         *tools.Registry
	runID            string
	parallelism      int
	policy           tools.Policy
	mu               sync.Mutex
	running          map[string]*runningTool
	completed        map[string]*types.ToolResult
	unknown          map[string]bool
	blocked          map[string]bool
	closed           bool
}

func newToolExecutor(runID string, registry *tools.Registry, parallelism int, policy tools.Policy) *toolExecutor {
	if parallelism < 1 {
		parallelism = 1
	}
	return &toolExecutor{registry: registry, runID: runID, parallelism: parallelism, policy: policy, running: make(map[string]*runningTool), completed: make(map[string]*types.ToolResult), unknown: make(map[string]bool), blocked: make(map[string]bool), started: make(map[string]time.Time), identities: make(map[string]types.ToolCall)}
}
func (e *toolExecutor) key(callID string) string { return e.runID + "\x00" + callID }
func (e *toolExecutor) execute(ctx context.Context, call types.ToolCall, resume *types.ResumeAnswer, emit types.ToolChunkSink) (*types.ToolResult, error) {
	if call.ID == "" {
		return nil, fmt.Errorf("tool call ID is required")
	}
	key := e.key(call.ID)
	e.mu.Lock()
	if original, exists := e.identities[key]; exists && (original.Name != call.Name || original.Arguments != call.Arguments) {
		e.mu.Unlock()
		return nil, fmt.Errorf("tool call %s changed after execution started", call.ID)
	}
	if result, ok := e.completed[key]; ok {
		copy := *result
		e.mu.Unlock()
		return &copy, nil
	}
	if running, ok := e.running[key]; ok {
		if running.call.Name != call.Name || running.call.Arguments != call.Arguments {
			e.mu.Unlock()
			return nil, fmt.Errorf("tool call %s changed after execution started", call.ID)
		}
		e.mu.Unlock()
		select {
		case <-running.done:
			return running.result, running.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if e.closed {
		e.mu.Unlock()
		return nil, context.Canceled
	}
	if e.unknown[key] {
		e.mu.Unlock()
		return nil, fmt.Errorf("tool call %s has unknown outcome; explicit reconciliation required", call.ID)
	}
	runCtx, cancel := context.WithCancel(ctx)
	running := &runningTool{done: make(chan struct{}), cancel: cancel, call: call}
	e.running[key] = running
	delete(e.blocked, key)
	e.identities[key] = call
	started := time.Now()
	e.started[key] = started
	e.mu.Unlock()
	result, err := func() (result *types.ToolResult, err error) {
		// Tool and middleware code may panic after producing a side effect.
		// Return a system error, then finalize the shared ledger below so waiters
		// and cleanup cannot remain blocked on this execution forever.
		defer func() {
			if recovered := recover(); recovered != nil {
				result = nil
				err = fmt.Errorf("tool %s panicked: %v", call.Name, recovered)
			}
		}()
		var invocationMu sync.Mutex
		var invoked *types.ToolCall
		var savedResult *types.ToolResult
		var savedErr error
		invoke := func(ctx context.Context, current types.ToolCall, options ...einotool.Option) (*types.ToolResult, error) {
			invocationMu.Lock()
			defer invocationMu.Unlock()
			if invoked != nil && (invoked.ID != current.ID || invoked.Name != current.Name || invoked.Arguments != current.Arguments) {
				return nil, fmt.Errorf("tool middleware changed an already executed call")
			}
			if invoked == nil {
				copy := current
				invoked = &copy
				savedResult, savedErr = e.invoke(ctx, current, resume, emit, options...)
			}
			if savedResult == nil {
				return nil, savedErr
			}
			copy := *savedResult
			return &copy, savedErr
		}
		endpoint := middleware.ToolHandler(func(ctx context.Context, call types.ToolCall) (*types.ToolResult, error) {
			descriptor, _ := e.registry.Lookup(call.Name)
			if _, enhanced := descriptor.Tool.(einotool.EnhancedStreamableTool); enhanced {
				return invoke(ctx, call)
			}
			if _, enhanced := descriptor.Tool.(einotool.EnhancedInvokableTool); enhanced {
				return invoke(ctx, call)
			}
			if _, streaming := descriptor.Tool.(einotool.StreamableTool); streaming {
				return invoke(ctx, call)
			}
			var result *types.ToolResult
			var resultMu sync.Mutex
			native := compose.InvokableToolEndpoint(func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				resultMu.Lock()
				defer resultMu.Unlock()
				if input == nil || input.CallID != call.ID || input.Name != call.Name {
					return nil, fmt.Errorf("tool middleware changed call identity")
				}
				modified := call
				modified.Arguments = input.Arguments
				var err error
				result, err = invoke(ctx, modified, input.CallOptions...)
				if err != nil {
					return nil, err
				}
				return &compose.ToolOutput{Result: result.Content}, nil
			})
			for i := len(e.middlewares) - 1; i >= 0; i-- {
				wrappers := e.middlewares[i].ToolCallMiddlewares()
				for j := len(wrappers) - 1; j >= 0; j-- {
					if wrappers[j].Invokable != nil {
						native = wrappers[j].Invokable(native)
					}
				}
			}
			output, err := native(ctx, &compose.ToolInput{Name: call.Name, CallID: call.ID, Arguments: call.Arguments})
			if err != nil {
				return nil, err
			}
			if output == nil {
				return nil, fmt.Errorf("tool middleware returned nil output")
			}
			resultMu.Lock()
			defer resultMu.Unlock()
			if result == nil {
				result = &types.ToolResult{CallID: call.ID, ReturnDirect: descriptor.ReturnDirect}
			}
			result.Content = output.Result
			return result, nil
		})
		for i := len(e.middlewares) - 1; i >= 0; i-- {
			if wrapper, ok := e.middlewares[i].(middleware.ToolMiddleware); ok {
				endpoint = wrapper.WrapTool(endpoint)
			}
		}
		if e.onStart != nil {
			if err := e.onStart(runCtx, types.ToolCallState{Call: call, Status: types.CallRunning, StartedAt: started}); err != nil {
				return nil, err
			}
		}
		result, err = endpoint(runCtx, call)
		if err == nil && result == nil {
			err = fmt.Errorf("tool %s returned no result", call.Name)
		}
		return result, err
	}()
	cancel()
	e.mu.Lock()
	running.result = result
	running.err = err
	if err == nil && result != nil {
		copy := *result
		e.completed[key] = &copy
	}
	if err != nil {
		_, interrupt := compose.IsInterruptRerunError(err)
		_, nested := compose.ExtractInterruptInfo(err)
		if interrupt || nested {
			e.blocked[key] = true
		} else {
			e.unknown[key] = true
		}
	}
	delete(e.running, key)
	close(running.done)
	e.mu.Unlock()
	return result, err
}
func (e *toolExecutor) authorize(ctx context.Context, call types.ToolCall, resume *types.ResumeAnswer) (tools.Descriptor, types.ToolCall, *types.ToolResult, error) {
	d, ok := e.registry.Lookup(call.Name)
	result := &types.ToolResult{CallID: call.ID, ReturnDirect: d.ReturnDirect}
	if !ok {
		result.IsError = true
		result.Content = "unknown tool: " + call.Name
		return d, call, result, nil
	}
	if err := ctx.Err(); err != nil {
		return d, call, nil, err
	}
	if d.NormalizeArgs != nil {
		args, err := d.NormalizeArgs(call.Arguments)
		if err != nil {
			result.IsError = true
			result.Content = err.Error()
			return d, call, result, nil
		}
		call.Arguments = args
	}
	decision := tools.Decision{Action: tools.Allow}
	if d.RequiresApproval {
		decision.Action = tools.AskApproval
	}
	if e.policy != nil {
		var err error
		decision, err = e.policy.Decide(ctx, call, d)
		if err != nil {
			return d, call, nil, err
		}
	}
	if state := types.RunStateFromContext(ctx); state != nil && decision.Action == tools.Allow {
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
					return d, call, nil, fmt.Errorf("approval call ID %q does not match %q", answer.CallID, call.ID)
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
			if state := types.RunStateFromContext(ctx); state != nil {
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
			return d, call, nil, compose.Interrupt(ctx, info)
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
		return d, call, result, nil
	}
	if decision.Action != tools.Allow {
		return d, call, nil, fmt.Errorf("invalid policy action %q", decision.Action)
	}
	if !d.ReadOnly && e.beforeInvoke != nil {
		if err := e.beforeInvoke(ctx, call); err != nil {
			return d, call, nil, err
		}
	}
	return d, call, nil, nil
}
func (e *toolExecutor) invoke(ctx context.Context, call types.ToolCall, resume *types.ResumeAnswer, emit types.ToolChunkSink, options ...einotool.Option) (*types.ToolResult, error) {
	ctx = context.WithValue(ctx, toolCallIDKey{}, call.ID)
	ctx = context.WithValue(ctx, toolExecutorKey{}, e)
	descriptor, _ := e.registry.Lookup(call.Name)
	if enhanced, ok := descriptor.Tool.(einotool.EnhancedStreamableTool); ok {
		return e.invokeEnhancedStream(ctx, call, resume, emit, enhanced, options...)
	}
	if enhanced, ok := descriptor.Tool.(einotool.EnhancedInvokableTool); ok {
		return e.invokeEnhanced(ctx, call, resume, enhanced, options...)
	}
	if streaming, ok := descriptor.Tool.(einotool.StreamableTool); ok {
		return e.invokeStream(ctx, call, resume, emit, streaming, options...)
	}
	descriptor, call, early, err := e.authorize(ctx, call, resume)
	if early != nil || err != nil {
		return early, err
	}
	result := &types.ToolResult{CallID: call.ID, ReturnDirect: descriptor.ReturnDirect}
	invokable, ok := descriptor.Tool.(einotool.InvokableTool)
	if !ok {
		return nil, fmt.Errorf("tool %s has no Eino execution interface", call.Name)
	}
	result.Content, err = invokable.InvokableRun(ctx, call.Arguments, options...)
	return finishToolResult(ctx, result, err)
}
func finishToolResult(ctx context.Context, result *types.ToolResult, err error) (*types.ToolResult, error) {
	if err != nil {
		var internal *types.InternalError
		if errors.As(err, &internal) {
			return nil, err
		}
		var delivery *types.EventDeliveryError
		if errors.As(err, &delivery) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		if _, ok := compose.ExtractInterruptInfo(err); ok {
			return nil, err
		}
		if _, ok := compose.IsInterruptRerunError(err); ok {
			return nil, err
		}
		result.IsError = true
		result.Content = err.Error()
	}
	return result, nil
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
		if !d.ParallelSafe || d.RequiresApproval || e.policy != nil {
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
			if !d.ParallelSafe || d.RequiresApproval || e.policy != nil {
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
	for _, call := range calls {
		key := e.key(call.Call.ID)
		e.identities[key] = call.Call
		if !call.StartedAt.IsZero() {
			e.started[key] = call.StartedAt
		}
		switch call.Status {
		case types.CallBlocked:
			e.blocked[key] = true
		case types.CallCompleted:
			if call.Result != nil {
				copy := *call.Result
				e.completed[key] = &copy
			}
		case types.CallRunning, types.CallOutcomeUnknown:
			e.unknown[key] = true
		}
	}
}
func (e *toolExecutor) cancel(ctx context.Context) error {
	e.mu.Lock()
	e.closed = true
	active := make([]*runningTool, 0, len(e.running))
	for _, running := range e.running {
		running.cancel()
		active = append(active, running)
	}
	e.mu.Unlock()
	for _, running := range active {
		select {
		case <-running.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	e.eager.Wait()
	return nil
}

// snapshot joins eager and ordinary execution results into the graph's state.
func (e *toolExecutor) snapshot(calls []types.ToolCallState) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for i := range calls {
		key := e.key(calls[i].Call.ID)
		if started, ok := e.started[key]; ok {
			calls[i].StartedAt = started
		}
		if result, ok := e.completed[key]; ok {
			copy := *result
			calls[i].Result = &copy
			calls[i].Status = types.CallCompleted
		} else if e.unknown[key] {
			calls[i].Status = types.CallOutcomeUnknown
		} else if _, ok := e.running[key]; ok {
			calls[i].Status = types.CallRunning
		} else if e.blocked[key] {
			calls[i].Status = types.CallBlocked
		} else {
			calls[i].Status = types.CallPending
		}
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
