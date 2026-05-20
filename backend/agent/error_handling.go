package agent

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type errorReason string

const (
	reasonQuota     errorReason = "quota"
	reasonAuth      errorReason = "auth"
	reasonTransient errorReason = "transient"
	reasonBusy      errorReason = "busy"
	reasonGeneric   errorReason = "generic"
)

type errorHandlingModel struct {
	m           model.BaseChatModel
	maxAttempts int
	baseDelay   time.Duration
	capDelay    time.Duration
	cb          *circuitBreaker
}

func wrapErrorHandling(m model.BaseChatModel) model.BaseChatModel {

	return &errorHandlingModel{
		m:           m,
		maxAttempts: 3,
		baseDelay:   time.Duration(1000) * time.Millisecond,
		capDelay:    time.Duration(8000) * time.Millisecond,
		cb: &circuitBreaker{
			threshold: 5,
			recovery:  time.Duration(60) * time.Second,
			state:     cbClosed,
		},
	}
}

func (e *errorHandlingModel) Generate(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	if e.cb.shouldFastFail() {
		return circuitOpenMessage(), nil
	}

	var (
		out    *schema.Message
		err    error
		reason errorReason
	)
	for attempt := 1; attempt <= e.maxAttempts; attempt++ {
		out, err = e.m.Generate(ctx, input, opts...)
		if err == nil {
			e.cb.recordSuccess()
			return out, nil
		}
		reason = classifyError(err)
		if reason != reasonTransient && reason != reasonBusy {
			break
		}
		if attempt == e.maxAttempts {
			break
		}
		sleepDuration := getBackoffDuration(e.baseDelay, e.capDelay, attempt)
		slog.Warn("LLM call failed; will retry",
			"reason", reason, "attempt", attempt, "max", e.maxAttempts,
			"sleep_ms", sleepDuration.Milliseconds(), "err", err)
		if waitErr := sleepCtx(ctx, sleepDuration); waitErr != nil {
			err = waitErr
			break
		}
	}

	e.cb.recordFailure()
	slog.Warn("LLM call failed; surfacing fallback assistant message",
		"reason", reason, "err", err)
	return getFallbackMessage(reason, err), nil
}

func (e *errorHandlingModel) Stream(
	ctx context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	msg, err := e.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func getBackoffDuration(baseDelay, capDelay time.Duration, attempt int) time.Duration {
	return min(baseDelay<<(attempt-1), capDelay)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	select {
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func getFallbackMessage(reason errorReason, err error) *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: getFallbackText(reason, err),
	}
}

func getFallbackText(reason errorReason, err error) string {
	switch reason {
	case reasonQuota:
		return "LLM provider rejected the request: account quota / billing problem. Please check the provider account and try again."
	case reasonAuth:
		return "LLM provider rejected the request: authentication or access is invalid. Please check the provider credentials and try again."
	case reasonBusy, reasonTransient:
		return "LLM provider is temporarily unavailable after multiple retries. Please wait a moment and continue the conversation."
	}
	if err != nil {
		return "LLM request failed: " + err.Error()
	}
	return "LLM request failed."
}

type circuitBreaker struct {
	threshold int
	recovery  time.Duration

	mu            sync.Mutex
	state         cbState
	failures      int
	openUntil     time.Time
	probeInFlight bool
}

type cbState string

const (
	cbClosed   cbState = "closed"
	cbHalfOpen cbState = "half_open"
	cbOpen     cbState = "open"
)

func circuitOpenMessage() *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: "LLM provider is currently unavailable due to continuous failures. Circuit breaker is engaged to protect the system. Please wait a moment before trying again.",
	}
}

func (cb *circuitBreaker) shouldFastFail() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	if cb.state == cbOpen {
		if time.Now().Before(cb.openUntil) {
			return true
		}
		cb.state = cbHalfOpen
		cb.probeInFlight = false
	}
	if cb.state == cbHalfOpen {
		if cb.probeInFlight {
			return true
		}
		cb.probeInFlight = true
	}
	return false
}

func (cb *circuitBreaker) recordSuccess() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures = 0
	cb.probeInFlight = false
	cb.state = cbClosed
}

func (cb *circuitBreaker) recordFailure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.failures++
	cb.probeInFlight = false
	if cb.failures >= cb.threshold {
		cb.state = cbOpen
		cb.openUntil = time.Now().Add(cb.recovery)
	}
}

var (
	quotaKeywords = []string{
		"insufficient_quota", "insufficient quota",
		"exceeded your current quota", "usage limit",
		"billing", "rate limit exceeded",
		"配额", "额度",
	}
	authKeywords = []string{
		"unauthorized", " 401", " 403",
		"invalid api key", "incorrect api key", "permission denied",
		"鉴权", "未授权", "无权限",
	}
	transientKeywords = []string{
		" 500", " 502", " 503", " 504",
		"timeout", "deadline exceeded",
		"connection reset", "connection refused",
		"服务不可用", "网关",
	}
	busyKeywords = []string{
		" 429", "too many requests",
		"model is overloaded", "rate_limit_reached",
		"服务繁忙", "请稍后",
	}
)

func containsAny(s string, needles []string) bool {
	for _, kw := range needles {
		if strings.Contains(s, kw) {
			return true
		}
	}
	return false
}

func classifyError(err error) errorReason {
	if err == nil {
		return reasonGeneric
	}
	detail := strings.ToLower(err.Error())
	switch {
	case containsAny(detail, quotaKeywords):
		return reasonQuota
	case containsAny(detail, authKeywords):
		return reasonAuth
	case containsAny(detail, transientKeywords):
		return reasonTransient
	case containsAny(detail, busyKeywords):
		return reasonBusy
	}
	return reasonGeneric
}
