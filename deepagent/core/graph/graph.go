package graph

import (
	"context"
	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"encoding/json"
	"errors"
	"fmt"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func (a *DeepAgent) buildGraph(ctx context.Context) error {
	g := compose.NewGraph[*types.RunState, *schema.Message](compose.WithGenLocalState(func(ctx context.Context) *types.RunState {
		// Initialize before Eino can interrupt at the first node. Resume uses
		// the checkpoint local state instead of invoking this generator.
		if state := types.RunStateFromContext(ctx); state != nil {
			return state
		}
		return &types.RunState{}
	}))
	// Each node obtains the restored local state. A fresh input must never replace
	// a checkpoint state, even when resume starts directly at model or tools.
	node := func(name string, fn func(context.Context, *types.RunState) (*types.RunState, error)) *compose.Lambda {
		return compose.InvokableLambda(func(ctx context.Context, input *types.RunState) (*types.RunState, error) {
			if initial, ok := ctx.Value(initialCheckpointKey{}).(*types.RunState); name == "prepare" && ok {
				fresh := false
				if err := compose.ProcessState[*types.RunState](ctx, func(_ context.Context, s *types.RunState) error {
					fresh = s == initial
					return nil
				}); err != nil {
					return nil, err
				}
				if fresh {
					a.mu.Lock()
					a.state = initial
					a.mu.Unlock()
					return nil, compose.Interrupt(ctx, &initialCheckpoint{})
				}
			}
			state, err := a.localState(ctx, input)
			if err != nil {
				return nil, err
			}
			if a.cfg.MaxSteps > 0 && state.GraphSteps >= a.cfg.MaxSteps {
				return nil, fmt.Errorf("maximum graph steps exceeded: %d", a.cfg.MaxSteps)
			}
			state.GraphSteps++
			ctx = types.WithRunState(ctx, state)
			ctx = types.WithEventSink(ctx, types.EventSinkFunc(func(ctx context.Context, event types.RuntimeEvent) error {
				return a.event(ctx, state, event.Kind, event.CallID, event.Data)
			}))
			output, nodeErr := fn(ctx, state)
			if a.cfg.Depth > 0 {
				raw, err := json.Marshal(a.conversation.History(ctx))
				if err != nil {
					return nil, err
				}
				if state.Extensions == nil {
					state.Extensions = map[string]json.RawMessage{}
				}
				state.Extensions["child_history"] = raw
				usage, err := json.Marshal(a.conversation.ContextUsage())
				if err != nil {
					return nil, err
				}
				state.Extensions["child_usage"] = usage
			}
			a.executor.snapshotChildCheckpoints(state)
			markRunError(ctx, state, nodeErr)
			if snapshotErr := a.graphState.SnapshotExtensions(state); snapshotErr != nil {
				state.Phase = types.PhaseFailed
				return nil, snapshotErr
			}
			return output, nodeErr
		})
	}
	for _, n := range []struct {
		name string
		fn   func(context.Context, *types.RunState) (*types.RunState, error)
	}{{"prepare", a.prepare}, {"model", a.callModel}, {"tools", a.callTools}, {"continue", a.continueRun}} {
		if err := g.AddLambdaNode(n.name, node(n.name, n.fn)); err != nil {
			return err
		}
	}
	if err := g.AddLambdaNode("finish", compose.InvokableLambda(a.finish)); err != nil {
		return err
	}
	for _, edge := range [][2]string{{compose.START, "prepare"}, {"prepare", "model"}, {"finish", compose.END}} {
		if err := g.AddEdge(edge[0], edge[1]); err != nil {
			return err
		}
	}
	if err := g.AddBranch("model", compose.NewGraphBranch(func(_ context.Context, s *types.RunState) (string, error) {
		if len(s.Calls) > 0 {
			return "tools", nil
		}
		return "continue", nil
	}, map[string]bool{"tools": true, "continue": true})); err != nil {
		return err
	}
	if err := g.AddBranch("tools", compose.NewGraphBranch(func(_ context.Context, s *types.RunState) (string, error) {
		for _, c := range s.Calls {
			if c.Result != nil && c.Result.ReturnDirect {
				return "continue", nil
			}
		}
		return "model", nil
	}, map[string]bool{"model": true, "continue": true})); err != nil {
		return err
	}
	if err := g.AddBranch("continue", compose.NewGraphBranch(func(_ context.Context, s *types.RunState) (string, error) {
		if s.Phase == types.PhasePreparing {
			return "prepare", nil
		}
		return "finish", nil
	}, map[string]bool{"prepare": true, "finish": true})); err != nil {
		return err
	}
	options := []compose.GraphCompileOption{compose.WithGraphName("deepagent"), compose.WithNodeTriggerMode(compose.AnyPredecessor), compose.WithMaxRunSteps(a.cfg.MaxSteps)}
	if a.cfg.CheckpointStore != nil {
		options = append(options, compose.WithCheckPointStore(checkpointer.New(a.cfg.CheckpointStore, a.cfg.ThreadID, a.runID, "core-graph-v1")))
	}
	if len(a.cfg.InterruptBeforeNodes) > 0 {
		options = append(options, compose.WithInterruptBeforeNodes(a.cfg.InterruptBeforeNodes))
	}
	if len(a.cfg.InterruptAfterNodes) > 0 {
		options = append(options, compose.WithInterruptAfterNodes(a.cfg.InterruptAfterNodes))
	}
	var err error
	a.graph, err = g.Compile(ctx, options...)
	return err
}
func (a *DeepAgent) localState(ctx context.Context, _ *types.RunState) (*types.RunState, error) {
	var state *types.RunState
	err := compose.ProcessState[*types.RunState](ctx, func(_ context.Context, s *types.RunState) error {
		if s.Version != 1 {
			return fmt.Errorf("unsupported run state version %d", s.Version)
		}
		if s.GraphSteps < 0 || s.ModelCalls < 0 {
			return fmt.Errorf("invalid negative run budget counters")
		}
		state = s
		return nil
	})
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	first := a.state != state
	a.state = state
	a.mu.Unlock()
	if first {
		if raw, ok := state.Extensions["child_history"]; ok && a.cfg.Depth > 0 {
			var messages []*schema.Message
			if err := json.Unmarshal(raw, &messages); err != nil {
				return nil, err
			}
			if err := a.conversation.AddHistory(ctx, state.RunID, messages...); err != nil {
				return nil, err
			}
			if raw, ok := state.Extensions["child_usage"]; ok {
				var usage conversation.ContextUsageSnapshot
				if err := json.Unmarshal(raw, &usage); err != nil {
					return nil, err
				}
				restorer, ok := a.conversation.(interface {
					RestoreUsage(context.Context, conversation.ContextUsageSnapshot) error
				})
				if !ok {
					return nil, fmt.Errorf("child conversation cannot restore usage")
				}
				if err := restorer.RestoreUsage(ctx, usage); err != nil {
					return nil, err
				}
			}
		}
		if raw, ok := state.Extensions["legacy_engine_history"]; ok {
			var expected []*schema.Message
			if err := json.Unmarshal(raw, &expected); err != nil {
				return nil, err
			}
			actual, err := json.Marshal(a.conversation.History(ctx))
			if err != nil {
				return nil, err
			}
			normalized, err := json.Marshal(expected)
			if err != nil {
				return nil, err
			}
			if string(actual) != string(normalized) {
				return nil, fmt.Errorf("legacy checkpoint requires matching durable conversation history")
			}
			delete(state.Extensions, "legacy_engine_history")
		}
		if raw, ok := state.Extensions["legacy_tools_message"]; ok {
			var expected schema.Message
			if err := json.Unmarshal(raw, &expected); err != nil {
				return nil, err
			}
			history := a.conversation.History(ctx)
			if len(history) == 0 {
				return nil, fmt.Errorf("legacy tools checkpoint requires durable conversation history")
			}
			last := history[len(history)-1]
			if last == nil || last.Role != schema.Assistant || len(last.ToolCalls) != len(expected.ToolCalls) {
				return nil, fmt.Errorf("legacy tools checkpoint history boundary mismatch")
			}
			for i, call := range expected.ToolCalls {
				actual := last.ToolCalls[i]
				if actual.ID != call.ID || actual.Function != call.Function {
					return nil, fmt.Errorf("legacy tool call %q does not match durable history", call.ID)
				}
			}
			delete(state.Extensions, "legacy_tools_message")
		}
		if err := a.graphState.RestoreExtensions(state); err != nil {
			return nil, err
		}
		if err := a.conversation.RestoreRunUsage(ctx, state.Usage); err != nil {
			return nil, err
		}
		a.executor.restore(state.Calls)
		a.executor.restoreChildCheckpoints(state)
		a.executor.onStart = func(ctx context.Context, call types.ToolCallState) error {
			return a.event(ctx, state, "tool_start", call.Call.ID, call)
		}
		if err := a.event(ctx, state, "run_state_restored", "", state.Consumed); err != nil {
			return nil, err
		}
	}
	return state, nil
}
func hasPendingInputs(s *types.RunState) bool {
	_, pending := s.Extensions["pending_inputs"]
	_, legacy := s.Extensions["legacy_pending_inputs"]
	return pending || legacy
}

func (a *DeepAgent) persistInputs(ctx context.Context, s *types.RunState) error {
	var prepared int
	if raw := s.Extensions["prepared_inputs"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &prepared); err != nil {
			return err
		}
	}
	if prepared < 0 || prepared > len(s.Consumed) {
		return fmt.Errorf("invalid prepared input cursor")
	}
	for _, input := range s.Consumed[prepared:] {
		if err := a.conversation.AddHistory(ctx, s.RunID, input.Message); err != nil {
			return err
		}
		prepared++
		if s.Extensions == nil {
			s.Extensions = make(map[string]json.RawMessage)
		}
		s.Extensions["prepared_inputs"], _ = json.Marshal(prepared)
		if err := a.event(ctx, s, "input_consumed", "", input); err != nil {
			return err
		}
	}
	delete(s.Extensions, "legacy_pending_inputs")
	delete(s.Extensions, "pending_inputs")
	return nil
}
func (a *DeepAgent) prepare(ctx context.Context, s *types.RunState) (*types.RunState, error) {
	if err := a.persistInputs(ctx, s); err != nil {
		return nil, err
	}
	if err := a.compactContext(ctx, s); err != nil {
		return nil, err
	}
	s.Phase = types.PhaseModeling
	return s, nil
}
func (a *DeepAgent) compactContext(ctx context.Context, s *types.RunState) error {
	if !a.conversation.CompactNeeded(ctx) {
		return nil
	}
	if err := a.event(ctx, s, "context_compact_started", "", conversation.ContextCompactStartedPayload{ContextUsage: a.conversation.ContextUsage()}); err != nil {
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
func (a *DeepAgent) callTools(ctx context.Context, s *types.RunState) (*types.RunState, error) {
	s.Phase = types.PhaseTools
	if err := a.prepareToolCalls(ctx, s); err != nil {
		return nil, err
	}
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
	messages, err := a.finishToolCalls(ctx, s, results)
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
	var persisted int
	if raw := s.Extensions["legacy_persisted_tool_results"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &persisted); err != nil || persisted < 0 || persisted > len(results) {
			return nil, fmt.Errorf("invalid legacy persisted tool cursor")
		}
	}
	for i, result := range results {
		if i < persisted {
			continue
		}
		message := messages[i]
		if err := a.conversation.AddHistory(ctx, s.RunID, message); err != nil {
			return nil, err
		}
		var callState types.ToolCallState
		for _, call := range s.Calls {
			if call.Call.ID == result.CallID {
				callState = call
				break
			}
		}
		if err := a.event(ctx, s, "tool_end", result.CallID, callState); err != nil {
			return nil, err
		}
	}
	delete(s.Extensions, "legacy_persisted_tool_results")
	s.Pending = nil
	return s, nil
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
func (a *DeepAgent) finish(ctx context.Context, s *types.RunState) (*schema.Message, error) {
	s.Pending = nil
	history := a.conversation.History(ctx)
	if len(history) == 0 {
		return nil, fmt.Errorf("agent produced no message")
	}
	message := history[len(history)-1]
	if message.Role == schema.Tool {
		message = CopyMessage(message)
		message.Role = schema.Assistant
		message.ToolCallID = ""
		if message.Content == "" {
			for _, part := range message.UserInputMultiContent {
				if part.Type == schema.ChatMessagePartTypeText {
					message.Content += part.Text
				}
			}
		}
		if a.chunk != nil {
			if err := a.chunk(ctx, message); err != nil {
				return nil, err
			}
		}
	}
	return message, nil
}

// Node errors set checkpoint-visible state. The final execution error is
// applied again after checkpoint saving and resource cleanup have completed.
func markRunError(ctx context.Context, state *types.RunState, err error) {
	if state == nil || err == nil {
		return
	}
	_, interrupt := compose.IsInterruptRerunError(err)
	_, nested := compose.ExtractInterruptInfo(err)
	if interrupt || nested {
		state.Phase = types.PhaseBlocked
	} else if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		state.Phase = types.PhaseInterrupted
	} else {
		state.Phase = types.PhaseFailed
	}
}
