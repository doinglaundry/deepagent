package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/schema"
)

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
	if err := json.Unmarshal([]byte(data), &state); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failures, m.openUntil, m.probe = state.Failures, state.OpenUntil, false
	return nil
}
func (m *CircuitBreaker) WrapModel(next ModelHandler) ModelHandler {
	return func(ctx context.Context, messages []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		if err := ctx.Err(); err != nil {
			return nil, err
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
