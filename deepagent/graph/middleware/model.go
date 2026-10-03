package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"eino-cli/deepagent/graph/types"

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
func (*LoopGuard) RequiresCompleteModelResponse() bool { return true }

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
	err := json.Unmarshal([]byte(raw), &state)
	if err != nil {
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

// ModelRetry retries only errors returned while opening the model stream.
// Once a stream is returned, all chunks and errors pass through unchanged.
type ModelRetry struct {
	BaseMiddleware
	MaxAttempts int
	Delay       time.Duration
	Retryable   func(error) bool
}

func (*ModelRetry) Name() string { return "model_retry" }

func (m *ModelRetry) WrapModel(next ModelHandler) ModelHandler {
	return func(ctx context.Context, messages []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		attempts := m.MaxAttempts
		if attempts < 1 {
			attempts = 1
		}
		for attempt := 0; attempt < attempts; attempt++ {
			contextErr := ctx.Err()
			if contextErr != nil {
				return nil, contextErr
			}
			stream, err := next(ctx, messages)
			if err == nil {
				return stream, nil
			}
			if stream != nil {
				stream.Close()
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			if attempt+1 == attempts || m.Retryable == nil || !m.Retryable(err) {
				return nil, err
			}
			if m.Delay > 0 {
				timer := time.NewTimer(m.Delay)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					return nil, ctx.Err()
				}
			}
		}
		panic("unreachable")
	}
}

var ErrCircuitOpen = errors.New("model circuit is open")

// CircuitBreaker limits consecutive failures to open a model stream. Receiving
// a stream does not retry or consume it; stream errors remain Graph errors.
type CircuitBreaker struct {
	BaseMiddleware
	Threshold int
	Recovery  time.Duration
	mu        sync.Mutex
	failures  int
	openUntil time.Time
	probe     bool
}

func (*CircuitBreaker) Name() string { return "circuit_breaker" }

func (m *CircuitBreaker) NewRun() Middleware {
	return &CircuitBreaker{Threshold: m.Threshold, Recovery: m.Recovery}
}

func (m *CircuitBreaker) BuildStateHandler() types.RunTimeStateful { return m }

func (m *CircuitBreaker) MarshalRuntimeState() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, _ := json.Marshal(struct {
		Failures  int
		OpenUntil time.Time
	}{m.failures, m.openUntil})
	return string(raw)
}

func (m *CircuitBreaker) UnmarshalRuntimeState(data string) error {
	var state struct {
		Failures  int
		OpenUntil time.Time
	}
	err := json.Unmarshal([]byte(data), &state)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures, m.openUntil, m.probe = state.Failures, state.OpenUntil, false
	return nil
}

func (m *CircuitBreaker) WrapModel(next ModelHandler) ModelHandler {
	return func(ctx context.Context, messages []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		contextErr := ctx.Err()
		if contextErr != nil {
			return nil, contextErr
		}
		m.mu.Lock()
		if m.probe || (!m.openUntil.IsZero() && time.Now().Before(m.openUntil)) {
			m.mu.Unlock()
			return nil, ErrCircuitOpen
		}
		probing := !m.openUntil.IsZero()
		if probing {
			m.probe = true
		}
		m.mu.Unlock()
		stream, err := next(ctx, messages)
		m.mu.Lock()
		defer m.mu.Unlock()
		if probing {
			m.probe = false
		}
		// Cancellation is not evidence of provider failure. Leave an expired
		// open circuit eligible for another probe.
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return stream, err
		}
		if err == nil {
			m.failures, m.openUntil = 0, time.Time{}
			return stream, nil
		}
		m.failures++
		threshold := m.Threshold
		if threshold < 1 {
			threshold = 3
		}
		if m.failures >= threshold {
			recovery := m.Recovery
			if recovery <= 0 {
				recovery = 30 * time.Second
			}
			m.openUntil = time.Now().Add(recovery)
		}
		return stream, err
	}
}
