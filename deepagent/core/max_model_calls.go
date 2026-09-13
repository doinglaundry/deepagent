package deepagents

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/schema"

	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/types"
)

const maxModelCallsStateName = "deepagents.max_model_calls"

// ErrExceedMaxModelCalls is returned before a ChatModel invocation when the
// configured model-call budget for the current DeepAgent run has been used up.
var ErrExceedMaxModelCalls = errors.New("exceeds max model calls")

type maxModelCallsMiddleware struct {
	middleware.BaseMiddleware
	state *maxModelCallsState
}

func newMaxModelCallsMiddleware(state *maxModelCallsState) *maxModelCallsMiddleware {
	return &maxModelCallsMiddleware{state: state}
}

func (m *maxModelCallsMiddleware) Name() string { return maxModelCallsStateName }

func (m *maxModelCallsMiddleware) BeforeAgent(ctx context.Context) error {
	if m != nil && m.state != nil {
		m.state.reset()
	}
	return nil
}

func (m *maxModelCallsMiddleware) ModifyModelRequest(ctx context.Context, initialContext []*schema.Message, messages []*schema.Message, state *types.GraphState) ([]*schema.Message, error) {
	if m == nil || m.state == nil || m.state.limit <= 0 {
		return messages, nil
	}
	budget := m.state
	if state == nil {
		return nil, fmt.Errorf("%w: graph state is missing", ErrExceedMaxModelCalls)
	}
	if stateful, ok := state.GetStateful(maxModelCallsStateName).(*maxModelCallsState); ok && stateful != nil {
		budget = stateful
	}
	if err := budget.consume(); err != nil {
		return nil, err
	}
	return messages, nil
}

type maxModelCallsState struct {
	mu       sync.Mutex
	limit    int
	consumed int
}

type maxModelCallsStateSnapshot struct {
	Consumed int `json:"consumed"`
}

func newMaxModelCallsState(limit int) *maxModelCallsState {
	return &maxModelCallsState{limit: limit}
}

func (s *maxModelCallsState) reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.consumed = 0
	s.mu.Unlock()
}

func (s *maxModelCallsState) consume() error {
	if s == nil || s.limit <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.consumed >= s.limit {
		return fmt.Errorf("%w: limit=%d consumed=%d", ErrExceedMaxModelCalls, s.limit, s.consumed)
	}
	s.consumed++
	return nil
}

func (s *maxModelCallsState) MarshalRuntimeState() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	snapshot := maxModelCallsStateSnapshot{Consumed: s.consumed}
	s.mu.Unlock()
	data, err := sonic.MarshalString(snapshot)
	if err != nil {
		return ""
	}
	return data
}

func (s *maxModelCallsState) UnmarshalRuntimeState(data string) error {
	if s == nil || data == "" {
		return nil
	}
	var snapshot maxModelCallsStateSnapshot
	if err := sonic.UnmarshalString(data, &snapshot); err != nil {
		return err
	}
	s.mu.Lock()
	s.consumed = snapshot.Consumed
	s.mu.Unlock()
	return nil
}

func applyMaxModelCalls(config *Config, middlewares []middleware.Middleware) (configured []middleware.Middleware) {
	if config == nil || config.MaxModelCalls <= 0 {
		return middlewares
	}
	state := newMaxModelCallsState(config.MaxModelCalls)
	configured = append(middlewares, newMaxModelCallsMiddleware(state))
	customState := make(map[string]types.RunTimeStateful, len(config.CustomGraphState)+1)
	for name, stateful := range config.CustomGraphState {
		customState[name] = stateful
	}
	customState[maxModelCallsStateName] = state
	config.CustomGraphState = customState
	return configured
}
