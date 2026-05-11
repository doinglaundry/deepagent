package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/config"
)

type mockChatModel struct {
	responses []mockResponse
	calls     int
}

type mockResponse struct {
	msg *schema.Message
	err error
}

// Generate cycles through responses; the last entry repeats forever so
// "always errors with X" tests don't need to spell out N copies.
func (m *mockChatModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	i := m.calls
	m.calls++
	if i >= len(m.responses) {
		i = len(m.responses) - 1
	}
	r := m.responses[i]
	return r.msg, r.err
}

func (m *mockChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errors.New("stream unused")
}

func errResp(text string) mockResponse           { return mockResponse{err: errors.New(text)} }
func okResp(content string) mockResponse         { return mockResponse{msg: &schema.Message{Role: schema.Assistant, Content: content}} }
func mockResponses(rs ...mockResponse) []mockResponse { return rs }

func newWrapper(t *testing.T, inner *mockChatModel, maxAttempts, cbThreshold int) *errorHandlingModel {
	t.Helper()
	cfg := config.ErrorHandling{
		Enabled:        true,
		Retry:          config.RetryConfig{MaxAttempts: maxAttempts, BaseDelayMS: 1, CapDelayMS: 4},
		CircuitBreaker: config.CircuitBreakerConfig{FailureThreshold: cbThreshold, RecoverySeconds: 60},
	}
	w, ok := wrapErrorHandling(inner, cfg).(*errorHandlingModel)
	if !ok {
		t.Fatalf("wrapErrorHandling did not return *errorHandlingModel")
	}
	return w
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want errorReason
	}{
		{"quota_keyword", errors.New("Insufficient quota"), reasonQuota},
		{"auth_chinese", errors.New("未授权访问"), reasonAuth},
		{"transient_status_code", errors.New("upstream returned 503 service unavailable"), reasonTransient},
		{"busy_chinese", errors.New("服务繁忙，请稍后重试"), reasonBusy},
		{"quota_beats_429", errors.New("429 insufficient_quota"), reasonQuota},
		{"generic", errors.New("weird thing"), reasonGeneric},
		{"nil", nil, reasonGeneric},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyError(tc.err); got != tc.want {
				t.Fatalf("classifyError(%v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestGetBackoffDuration_ExponentialClamped(t *testing.T) {
	base, capDelay := 1*time.Second, 8*time.Second
	want := []time.Duration{1, 2, 4, 8, 8}
	for i, w := range want {
		got := getBackoffDuration(base, capDelay, i+1)
		if got != w*time.Second {
			t.Errorf("attempt=%d: got %v, want %v", i+1, got, w*time.Second)
		}
	}
}

func TestGenerate_SuccessFirstTry(t *testing.T) {
	inner := &mockChatModel{responses: mockResponses(okResp("ok"))}
	w := newWrapper(t, inner, 3, 999)
	msg, err := w.Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if msg.Content != "ok" || inner.calls != 1 {
		t.Errorf("content=%q calls=%d, want ok/1", msg.Content, inner.calls)
	}
}

func TestGenerate_TransientThenSuccess(t *testing.T) {
	inner := &mockChatModel{responses: mockResponses(errResp(" 503 transient"), okResp("ok"))}
	w := newWrapper(t, inner, 3, 999)
	msg, err := w.Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if msg.Content != "ok" || inner.calls != 2 {
		t.Errorf("content=%q calls=%d, want ok/2", msg.Content, inner.calls)
	}
}

func TestGenerate_TransientExhausted(t *testing.T) {
	inner := &mockChatModel{responses: mockResponses(errResp(" 503 transient"))}
	w := newWrapper(t, inner, 3, 999)
	msg, err := w.Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if inner.calls != 3 {
		t.Errorf("calls = %d, want 3", inner.calls)
	}
	if msg.Role != schema.Assistant || msg.ToolCalls != nil {
		t.Errorf("fallback must be Assistant/nil-tool-calls; got role=%v tc=%v", msg.Role, msg.ToolCalls)
	}
	if !strings.Contains(strings.ToLower(msg.Content), "retries") {
		t.Errorf("fallback should cite retries; got %q", msg.Content)
	}
}

func TestGenerate_NonRetryableNoRetry(t *testing.T) {
	inner := &mockChatModel{responses: mockResponses(errResp("Insufficient quota"))}
	w := newWrapper(t, inner, 3, 999)
	msg, err := w.Generate(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if inner.calls != 1 {
		t.Errorf("calls = %d, want 1 (quota must not retry)", inner.calls)
	}
	if !strings.Contains(strings.ToLower(msg.Content), "quota") {
		t.Errorf("fallback should cite quota; got %q", msg.Content)
	}
}

func TestGenerate_CtxCanceledDuringBackoff(t *testing.T) {
	cfg := config.ErrorHandling{
		Enabled:        true,
		Retry:          config.RetryConfig{MaxAttempts: 3, BaseDelayMS: 50, CapDelayMS: 200},
		CircuitBreaker: config.CircuitBreakerConfig{FailureThreshold: 999, RecoverySeconds: 60},
	}
	inner := &mockChatModel{responses: mockResponses(errResp(" 503"))}
	w := wrapErrorHandling(inner, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if _, err := w.Generate(ctx, nil); err != nil {
		t.Fatalf("wrapper should swallow ctx err and surface fallback; got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Errorf("ctx cancel should short-circuit backoff; took %v", elapsed)
	}
}

func TestCircuitBreaker_ClosedToOpenOnThreshold(t *testing.T) {
	cb := &circuitBreaker{threshold: 3, recovery: time.Hour, state: cbClosed}
	for range 3 {
		cb.recordFailure()
	}
	if !cb.shouldFastFail() {
		t.Fatal("expected fast-fail after threshold consecutive failures")
	}
}

func TestCircuitBreaker_RecordSuccessResetsCounter(t *testing.T) {
	cb := &circuitBreaker{threshold: 3, recovery: time.Hour, state: cbHalfOpen}
	for range 5 {
		cb.recordFailure()
	}
	cb.state = cbHalfOpen
	cb.recordSuccess()
	if cb.shouldFastFail() {
		t.Error("recordSuccess should restore closed state")
	}
	if cb.failures != 0 {
		t.Errorf("failures = %d, want 0", cb.failures)
	}
}

func TestCircuitBreaker_HalfOpenProbeInFlightFastFails(t *testing.T) {
	cb := &circuitBreaker{threshold: 1, recovery: time.Hour, state: cbHalfOpen}
	if cb.shouldFastFail() {
		t.Fatal("first half-open check should let the probe through")
	}
	if !cb.shouldFastFail() {
		t.Error("second half-open check must fast-fail while probe is in flight")
	}
}

func TestGenerate_QuotaCountsTowardCircuitBreaker(t *testing.T) {
	inner := &mockChatModel{responses: mockResponses(errResp("Insufficient quota"))}
	w := newWrapper(t, inner, 1, 3)
	for range 3 {
		if _, err := w.Generate(context.Background(), nil); err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
	}
	if !w.cb.shouldFastFail() {
		t.Error("quota errors must count toward circuit breaker (deer-flow alignment)")
	}
}

func TestWrapErrorHandling_DisabledReturnsInner(t *testing.T) {
	inner := &mockChatModel{}
	cases := []config.ErrorHandling{
		{Enabled: false, Retry: config.RetryConfig{MaxAttempts: 3}},
		{Enabled: true, Retry: config.RetryConfig{MaxAttempts: 0}},
	}
	for _, cfg := range cases {
		got, ok := wrapErrorHandling(inner, cfg).(*mockChatModel)
		if !ok || got != inner {
			t.Errorf("cfg=%+v: should return inner unchanged", cfg)
		}
	}
}
