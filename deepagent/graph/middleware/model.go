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
	messagepkg "eino-cli/deepagent/message"

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

func (*LoopGuard) GetName() string { return "loop_guard" }

func (loopGuard *LoopGuard) NewRun() Middleware {
	runLoopGuard := NewLoopGuard()
	if loopGuard.WarnThreshold > 0 {
		runLoopGuard.WarnThreshold = loopGuard.WarnThreshold
	}
	if loopGuard.HardLimit > 0 {
		runLoopGuard.HardLimit = loopGuard.HardLimit
	}
	if loopGuard.WindowSize > 0 {
		runLoopGuard.WindowSize = loopGuard.WindowSize
	}
	if loopGuard.Logger != nil {
		runLoopGuard.Logger = loopGuard.Logger
	}
	return runLoopGuard
}

// A decision hashes the complete batch, so eager execution must wait for it.
func (*LoopGuard) RequiresCompleteModelResponse() bool { return true }

func (loopGuard *LoopGuard) GetStateHandler() types.RunTimeStateful { return loopGuard }

func (loopGuard *LoopGuard) ModifyModelResponse(ctx context.Context, message *messagepkg.Message, _ *types.GraphState) (*messagepkg.Message, error) {
	if message == nil || len(message.ToolCalls) == 0 {
		return message, nil
	}
	batchHash := sha256.New()
	for _, toolCall := range message.ToolCalls {
		batchHash.Write([]byte(toolCall.Function.Name))
		batchHash.Write([]byte{0})
		batchHash.Write([]byte(toolCall.Function.Arguments))
		batchHash.Write([]byte{0})
	}
	batchKey := hex.EncodeToString(batchHash.Sum(nil)[:8])
	loopGuard.mu.Lock()
	loopGuard.recent = append(loopGuard.recent, batchKey)
	if len(loopGuard.recent) > loopGuard.WindowSize {
		loopGuard.recent = loopGuard.recent[len(loopGuard.recent)-loopGuard.WindowSize:]
	}
	repeatCount := 0
	for _, recentKey := range loopGuard.recent {
		if recentKey == batchKey {
			repeatCount++
		}
	}
	shouldWarn := repeatCount >= loopGuard.WarnThreshold && !loopGuard.warned[batchKey]
	if shouldWarn {
		loopGuard.warned[batchKey] = true
	}
	reachedHardLimit := repeatCount >= loopGuard.HardLimit
	loopGuard.mu.Unlock()
	if shouldWarn {
		loopGuard.Logger.WarnContext(ctx, "repeated tool call batch", "count", repeatCount, "limit", loopGuard.HardLimit)
	}
	if reachedHardLimit {
		messageCopy := *message
		messageCopy.ToolCalls = nil
		return &messageCopy, nil
	}
	return message, nil
}

func (loopGuard *LoopGuard) MarshalRuntimeState() string {
	loopGuard.mu.Lock()
	defer loopGuard.mu.Unlock()
	encodedState, _ := json.Marshal(struct {
		Recent []string
		Warned map[string]bool
	}{loopGuard.recent, loopGuard.warned})
	return string(encodedState)
}

func (loopGuard *LoopGuard) UnmarshalRuntimeState(encodedState string) error {
	var runtimeState struct {
		Recent []string
		Warned map[string]bool
	}
	err := json.Unmarshal([]byte(encodedState), &runtimeState)
	if err != nil {
		return err
	}
	loopGuard.mu.Lock()
	defer loopGuard.mu.Unlock()
	loopGuard.recent = runtimeState.Recent
	loopGuard.warned = runtimeState.Warned
	if loopGuard.warned == nil {
		loopGuard.warned = map[string]bool{}
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

func (*ModelRetry) GetName() string { return "model_retry" }

func (modelRetry *ModelRetry) WrapModel(nextModel ModelHandler) ModelHandler {
	return func(ctx context.Context, messages []*messagepkg.Message) (*schema.StreamReader[*messagepkg.Message], error) {
		maxAttempts := modelRetry.MaxAttempts
		if maxAttempts < 1 {
			maxAttempts = 1
		}
		for attempt := 0; attempt < maxAttempts; attempt++ {
			contextErr := ctx.Err()
			if contextErr != nil {
				return nil, contextErr
			}
			stream, err := nextModel(ctx, messages)
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
			if attempt+1 == maxAttempts || modelRetry.Retryable == nil || !modelRetry.Retryable(err) {
				return nil, err
			}
			if modelRetry.Delay > 0 {
				timer := time.NewTimer(modelRetry.Delay)
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

func (*CircuitBreaker) GetName() string { return "circuit_breaker" }

func (circuitBreaker *CircuitBreaker) NewRun() Middleware {
	return &CircuitBreaker{Threshold: circuitBreaker.Threshold, Recovery: circuitBreaker.Recovery}
}

func (circuitBreaker *CircuitBreaker) GetStateHandler() types.RunTimeStateful {
	return circuitBreaker
}

func (circuitBreaker *CircuitBreaker) MarshalRuntimeState() string {
	circuitBreaker.mu.Lock()
	defer circuitBreaker.mu.Unlock()
	encodedState, _ := json.Marshal(struct {
		Failures  int
		OpenUntil time.Time
	}{circuitBreaker.failures, circuitBreaker.openUntil})
	return string(encodedState)
}

func (circuitBreaker *CircuitBreaker) UnmarshalRuntimeState(encodedState string) error {
	var runtimeState struct {
		Failures  int
		OpenUntil time.Time
	}
	err := json.Unmarshal([]byte(encodedState), &runtimeState)
	if err != nil {
		return err
	}
	circuitBreaker.mu.Lock()
	defer circuitBreaker.mu.Unlock()
	circuitBreaker.failures, circuitBreaker.openUntil, circuitBreaker.probe = runtimeState.Failures, runtimeState.OpenUntil, false
	return nil
}

func (circuitBreaker *CircuitBreaker) WrapModel(nextModel ModelHandler) ModelHandler {
	return func(ctx context.Context, messages []*messagepkg.Message) (*schema.StreamReader[*messagepkg.Message], error) {
		contextErr := ctx.Err()
		if contextErr != nil {
			return nil, contextErr
		}
		circuitBreaker.mu.Lock()
		if circuitBreaker.probe || (!circuitBreaker.openUntil.IsZero() && time.Now().Before(circuitBreaker.openUntil)) {
			circuitBreaker.mu.Unlock()
			return nil, ErrCircuitOpen
		}
		isProbe := !circuitBreaker.openUntil.IsZero()
		if isProbe {
			circuitBreaker.probe = true
		}
		circuitBreaker.mu.Unlock()
		stream, err := nextModel(ctx, messages)
		circuitBreaker.mu.Lock()
		defer circuitBreaker.mu.Unlock()
		if isProbe {
			circuitBreaker.probe = false
		}
		// Cancellation is not evidence of provider failure. Leave an expired
		// open circuit eligible for another probe.
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return stream, err
		}
		if err == nil {
			circuitBreaker.failures, circuitBreaker.openUntil = 0, time.Time{}
			return stream, nil
		}
		circuitBreaker.failures++
		failureThreshold := circuitBreaker.Threshold
		if failureThreshold < 1 {
			failureThreshold = 3
		}
		if circuitBreaker.failures >= failureThreshold {
			recoveryDelay := circuitBreaker.Recovery
			if recoveryDelay <= 0 {
				recoveryDelay = 30 * time.Second
			}
			circuitBreaker.openUntil = time.Now().Add(recoveryDelay)
		}
		return stream, err
	}
}
