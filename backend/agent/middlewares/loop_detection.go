package middlewares

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// Defaults mirror the Python loop_detection_middleware constants.
const (
	defaultLoopWarnThreshold = 3
	defaultLoopHardLimit     = 5
	defaultLoopWindowSize    = 20
)

// LoopDetection mirrors
// deerflow.agents.middlewares.loop_detection_middleware. It hashes each
// assistant message's tool calls (name + args) into a sliding window and:
//   - logs a warning when the same hash appears WarnThreshold+ times.
//   - clears tool_calls when the hash hits HardLimit, forcing the agent to
//     produce a text response instead of looping forever.
//
// Phase 1 ships the in-memory single-thread variant. The full LRU per-thread
// tracking lands in Phase 2 alongside the rest of the "always-on"
// middlewares.
type LoopDetection struct {
	*adk.BaseChatModelAgentMiddleware

	WarnThreshold int
	HardLimit     int
	WindowSize    int
	Logger        *slog.Logger

	mu     sync.Mutex
	window []string
	warned map[string]struct{}
}

// NewLoopDetection returns a LoopDetection middleware with default thresholds.
func NewLoopDetection() *LoopDetection {
	return &LoopDetection{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		WarnThreshold:                defaultLoopWarnThreshold,
		HardLimit:                    defaultLoopHardLimit,
		WindowSize:                   defaultLoopWindowSize,
		Logger:                       slog.Default(),
		warned:                       map[string]struct{}{},
	}
}

func (m *LoopDetection) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	mc *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}
	last := state.Messages[len(state.Messages)-1]
	if last == nil || last.Role != schema.Assistant || len(last.ToolCalls) == 0 {
		return ctx, state, nil
	}

	hash := hashToolCalls(last.ToolCalls)
	count := m.recordAndCount(hash)

	switch {
	case count >= m.HardLimit:
		m.Logger.Warn("loop detection: hard limit hit, stripping tool_calls",
			"hash", hash, "count", count)
		// Force the model into a text-only response next turn.
		last.ToolCalls = nil
	case count >= m.WarnThreshold:
		m.warnOnce(hash, count)
	}
	return ctx, state, nil
}

func (m *LoopDetection) recordAndCount(hash string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.window = append(m.window, hash)
	if len(m.window) > m.WindowSize {
		m.window = m.window[len(m.window)-m.WindowSize:]
	}
	count := 0
	for _, h := range m.window {
		if h == hash {
			count++
		}
	}
	return count
}

func (m *LoopDetection) warnOnce(hash string, count int) {
	m.mu.Lock()
	_, seen := m.warned[hash]
	if !seen {
		m.warned[hash] = struct{}{}
	}
	m.mu.Unlock()
	if seen {
		return
	}
	m.Logger.Warn("loop detection: repeated tool call",
		"hash", hash, "count", count, "threshold", m.WarnThreshold)
}

func hashToolCalls(calls []schema.ToolCall) string {
	h := sha256.New()
	for _, c := range calls {
		h.Write([]byte(c.Function.Name))
		h.Write([]byte{0})
		h.Write([]byte(c.Function.Arguments))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}
