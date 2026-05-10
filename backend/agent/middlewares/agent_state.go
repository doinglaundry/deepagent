package middlewares

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
)

// AgentState tracks lifecycle counters (model-call, tool-call) for observers.
type AgentState struct {
	*adk.BaseChatModelAgentMiddleware

	Logger *slog.Logger

	mu      sync.Mutex
	state   AgentStateSnapshot
	startAt time.Time
}

// AgentStateSnapshot is the read-only view of AgentState's counters.
type AgentStateSnapshot struct {
	ModelCalls int
	ToolCalls  int
	StartedAt  time.Time
	UpdatedAt  time.Time
}

// NewAgentState returns an AgentState middleware ready for use.
func NewAgentState() *AgentState {
	now := time.Now()
	return &AgentState{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		Logger:                       slog.Default(),
		state:                        AgentStateSnapshot{StartedAt: now, UpdatedAt: now},
		startAt:                      now,
	}
}

func (m *AgentState) Snapshot() AgentStateSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

func (m *AgentState) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelCtx *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	m.mu.Lock()
	m.state.ModelCalls++
	m.state.UpdatedAt = time.Now()
	m.mu.Unlock()
	return ctx, state, nil
}

func (m *AgentState) AfterToolCallsRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	toolCallsCtx *adk.ToolCallsContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	count := 0
	if toolCallsCtx != nil {
		count = len(toolCallsCtx.ToolCalls)
	}
	m.mu.Lock()
	m.state.ToolCalls += count
	m.state.UpdatedAt = time.Now()
	m.mu.Unlock()
	return ctx, state, nil
}
