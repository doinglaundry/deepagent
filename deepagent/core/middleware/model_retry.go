package middleware

import (
	"context"
	"errors"
	"time"

	"github.com/cloudwego/eino/schema"
)

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
			if err := ctx.Err(); err != nil {
				return nil, err
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
