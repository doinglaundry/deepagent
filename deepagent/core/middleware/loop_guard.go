package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

type LoopGuard struct {
	BaseMiddleware
	WarnThreshold, HardLimit, WindowSize int
	Logger                               *slog.Logger
	mu                                   sync.Mutex
	recent                               []string
	warned                               map[string]bool
}

func NewLoopGuard() *LoopGuard {
	return &LoopGuard{WarnThreshold: 3, HardLimit: 5, WindowSize: 20, Logger: slog.Default(), warned: map[string]bool{}}
}
func (*LoopGuard) Name() string { return "loop_guard" }
func (m *LoopGuard) NewRun() Middleware {
	n := NewLoopGuard()
	if m.WarnThreshold > 0 {
		n.WarnThreshold = m.WarnThreshold
	}
	if m.HardLimit > 0 {
		n.HardLimit = m.HardLimit
	}
	if m.WindowSize > 0 {
		n.WindowSize = m.WindowSize
	}
	if m.Logger != nil {
		n.Logger = m.Logger
	}
	return n
}

// A decision hashes the complete batch, so eager execution must wait for it.
func (*LoopGuard) RequiresCompleteModelResponse() bool        { return true }
func (m *LoopGuard) BuildStateHandler() types.RunTimeStateful { return m }
func (m *LoopGuard) ModifyModelResponse(ctx context.Context, message *schema.Message, _ *types.GraphState) (*schema.Message, error) {
	if message == nil || len(message.ToolCalls) == 0 {
		return message, nil
	}
	hash := sha256.New()
	for _, call := range message.ToolCalls {
		hash.Write([]byte(call.Function.Name))
		hash.Write([]byte{0})
		hash.Write([]byte(call.Function.Arguments))
		hash.Write([]byte{0})
	}
	key := hex.EncodeToString(hash.Sum(nil)[:8])
	m.mu.Lock()
	m.recent = append(m.recent, key)
	if len(m.recent) > m.WindowSize {
		m.recent = m.recent[len(m.recent)-m.WindowSize:]
	}
	count := 0
	for _, item := range m.recent {
		if item == key {
			count++
		}
	}
	warn := count >= m.WarnThreshold && !m.warned[key]
	if warn {
		m.warned[key] = true
	}
	hard := count >= m.HardLimit
	m.mu.Unlock()
	if warn {
		m.Logger.WarnContext(ctx, "repeated tool call batch", "count", count, "limit", m.HardLimit)
	}
	if hard {
		copy := *message
		copy.ToolCalls = nil
		return &copy, nil
	}
	return message, nil
}
func (m *LoopGuard) MarshalRuntimeState() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, _ := json.Marshal(struct {
		Recent []string
		Warned map[string]bool
	}{m.recent, m.warned})
	return string(raw)
}
func (m *LoopGuard) UnmarshalRuntimeState(raw string) error {
	var state struct {
		Recent []string
		Warned map[string]bool
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recent = state.Recent
	m.warned = state.Warned
	if m.warned == nil {
		m.warned = map[string]bool{}
	}
	return nil
}
