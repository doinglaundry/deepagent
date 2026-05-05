package middlewares

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
)

// AgentState mirrors deerflow.agents.middlewares.agent_state_middleware.
// It tracks lifecycle counters (turn count, tool-call count, model-call
// count) so other middlewares and the REPL can observe how the agent is
// progressing without scraping the message stream themselves.
//
// Phase 2 surfaces the counters via Snapshot(); subsequent phases hook the
// values into session metadata and trace tags.
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
	mc *adk.ModelContext,
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
	tc *adk.ToolCallsContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	count := 0
	if tc != nil {
		count = len(tc.ToolCalls)
	}
	m.mu.Lock()
	m.state.ToolCalls += count
	m.state.UpdatedAt = time.Now()
	m.mu.Unlock()
	return ctx, state, nil
}
