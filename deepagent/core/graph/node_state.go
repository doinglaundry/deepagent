package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// Eino uses this value for a new run. A resumed run uses its checkpoint state.
func (a *DeepAgent) newLocalState(ctx context.Context) *types.RunState {
	state := types.RunStateFromContext(ctx)
	if state != nil {
		return state
	}
	return &types.RunState{}
}

// The first prepare establishes a durable Eino cursor before doing any work.
func (a *DeepAgent) ensureInitialCheckpoint(ctx context.Context) error {
	initial, ok := ctx.Value(initialCheckpointKey{}).(*types.RunState)
	if !ok {
		return nil
	}
	fresh := false
	err := compose.ProcessState[*types.RunState](ctx, func(_ context.Context, state *types.RunState) error {
		fresh = state == initial
		return nil
	})
	if err != nil {
		return err
	}
	if !fresh {
		return nil
	}
	a.mu.Lock()
	a.state = initial
	a.mu.Unlock()
	return compose.Interrupt(ctx, &initialCheckpoint{})
}

// enterNode always uses Eino's local state, including the restored state.
func (a *DeepAgent) enterNode(ctx context.Context, input *types.RunState) (context.Context, *types.RunState, error) {
	state, err := a.localState(ctx, input)
	if err != nil {
		return ctx, nil, err
	}
	if a.cfg.MaxSteps > 0 && state.GraphSteps >= a.cfg.MaxSteps {
		return ctx, nil, fmt.Errorf("maximum graph steps exceeded: %d", a.cfg.MaxSteps)
	}
	state.GraphSteps++
	ctx = types.WithRunState(ctx, state)
	ctx = types.WithEventSink(ctx, types.EventSinkFunc(func(ctx context.Context, event types.RuntimeEvent) error {
		return a.event(ctx, state, event.Kind, event.CallID, event.Data)
	}))
	return ctx, state, nil
}

func (a *DeepAgent) leaveNode(ctx context.Context, state, output *types.RunState, nodeErr error) (*types.RunState, error) {
	if a.cfg.Depth > 0 {
		history, err := json.Marshal(a.conversation.History(ctx))
		if err != nil {
			return nil, err
		}
		if state.Extensions == nil {
			state.Extensions = map[string]json.RawMessage{}
		}
		state.Extensions["child_history"] = history
		usage, err := json.Marshal(a.conversation.ContextUsage())
		if err != nil {
			return nil, err
		}
		state.Extensions["child_usage"] = usage
	}
	a.executor.snapshotChildCheckpoints(state)
	markRunError(ctx, state, nodeErr)
	snapshotErr := a.graphState.SnapshotExtensions(state)
	if snapshotErr != nil {
		state.Phase = types.PhaseFailed
		return nil, snapshotErr
	}
	return output, nodeErr
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
		raw, ok := state.Extensions["child_history"]
		if ok && a.cfg.Depth > 0 {
			var messages []*schema.Message
			err = json.Unmarshal(raw, &messages)
			if err != nil {
				return nil, err
			}
			err = a.conversation.AddHistory(ctx, state.RunID, messages...)
			if err != nil {
				return nil, err
			}
			raw, ok = state.Extensions["child_usage"]
			if ok {
				var usage conversation.ContextUsageSnapshot
				err = json.Unmarshal(raw, &usage)
				if err != nil {
					return nil, err
				}
				restorer, ok := a.conversation.(interface {
					RestoreUsage(context.Context, conversation.ContextUsageSnapshot) error
				})
				if !ok {
					return nil, fmt.Errorf("child conversation cannot restore usage")
				}
				err = restorer.RestoreUsage(ctx, usage)
				if err != nil {
					return nil, err
				}
			}
		}
		raw, ok = state.Extensions["legacy_engine_history"]
		if ok {
			var expected []*schema.Message
			err = json.Unmarshal(raw, &expected)
			if err != nil {
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
		raw, ok = state.Extensions["legacy_tools_message"]
		if ok {
			var expected schema.Message
			err = json.Unmarshal(raw, &expected)
			if err != nil {
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
		err = a.graphState.RestoreExtensions(state)
		if err != nil {
			return nil, err
		}
		err = a.conversation.RestoreRunUsage(ctx, state.Usage)
		if err != nil {
			return nil, err
		}
		a.executor.restore(state.Calls)
		a.executor.restoreChildCheckpoints(state)
		a.executor.onStart = func(ctx context.Context, call types.ToolCallState) error {
			return a.event(ctx, state, "tool_start", call.Call.ID, call)
		}
		err = a.event(ctx, state, "run_state_restored", "", state.Consumed)
		if err != nil {
			return nil, err
		}
	}
	return state, nil
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
