package execution

import (
	"context"
	"eino-cli/deepagent/graph/types"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func (a *Graph) newRunState(input []*schema.Message, options RunOptions) *types.RunState {
	state := &types.RunState{Version: 1, ThreadID: a.cfg.ThreadID, RunID: a.runID, AgentName: a.cfg.Name, Depth: a.cfg.Depth, Phase: types.PhasePreparing}
	for i, message := range input {
		if message != nil {
			entry := types.Input{Message: message}
			if i < len(options.InputIDs) {
				entry.MessageID = options.InputIDs[i]
			}
			if i < len(options.InputMeta) {
				entry.Meta = options.InputMeta[i]
			}
			state.Consumed = append(state.Consumed, entry)
		}
	}

	return state
}

// localState reads the authoritative RunState from Eino, then restores runtime
// collaborators once for each state object seen by this agent.
func (a *Graph) localState(ctx context.Context) (*types.RunState, error) {
	var state *types.RunState
	err := compose.ProcessState[*types.RunState](ctx, func(_ context.Context, s *types.RunState) error {
		if s.Version != 1 {
			return fmt.Errorf("unsupported run state version %d", s.Version)
		}
		if s.PreparedInputs < 0 || s.PreparedInputs > len(s.Consumed) {
			return fmt.Errorf("invalid prepared input cursor")
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
		err = a.restoreLocalState(ctx, state)
		if err != nil {
			return nil, err
		}
	}
	return state, nil
}

func (a *Graph) restoreLocalState(ctx context.Context, state *types.RunState) error {
	err := a.restoreChildConversation(ctx, state)
	if err != nil {
		return err
	}
	if state.Context != nil {
		err = a.conversation.RestoreContext(ctx, *state.Context)
		if err != nil {
			return err
		}
	}
	err = a.graphState.RestoreExtensions(state)
	if err != nil {
		return err
	}
	err = a.conversation.RestoreRunUsage(ctx, state.Usage)
	if err != nil {
		return err
	}
	a.executor.restore(state.Calls)
	a.executor.restoreChildCheckpoints(state)
	a.executor.onToolStart = func(ctx context.Context, call types.ToolCallState) error {
		return a.event(ctx, state, "tool_start", call.Call.ID, types.ToolStartPayload{Name: call.Call.Name, CallID: call.Call.ID, Args: call.Call.Arguments, ToolStartTime: call.StartedAt})
	}
	return a.event(ctx, state, "run_state_restored", "", state.Consumed)
}

func (a *Graph) restoreChildConversation(ctx context.Context, state *types.RunState) error {
	if a.cfg.Depth <= 0 {
		return nil
	}
	raw, ok := state.Extensions["child_history"]
	if !ok {
		return nil
	}
	var messages []*schema.Message
	err := json.Unmarshal(raw, &messages)
	if err != nil {
		return err
	}
	err = a.conversation.AddHistory(ctx, state.RunID, messages...)
	if err != nil {
		return err
	}
	return nil
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
