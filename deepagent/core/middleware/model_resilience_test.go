package middleware

import (
	"context"
	"errors"
	"github.com/cloudwego/eino/schema"
	"testing"
	"time"
)

func TestModelRetryOnlyRetriesBeforeStream(t *testing.T) {
	sentinel := errors.New("temporary model failure")
	retry := &ModelRetry{MaxAttempts: 3, Retryable: func(err error) bool { return errors.Is(err, sentinel) }}
	attempts := 0
	endpoint := retry.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		attempts++
		if attempts == 1 {
			return nil, sentinel
		}
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
	})
	stream, err := endpoint(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
	if attempts != 2 {
		t.Fatalf("attempts=%d", attempts)
	}
	attempts = 0
	endpoint = retry.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		attempts++
		reader, writer := schema.Pipe[*schema.Message](2)
		writer.Send(schema.AssistantMessage("partial", nil), nil)
		writer.Send(nil, sentinel)
		writer.Close()
		return reader, nil
	})
	stream, err = endpoint(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if chunk, err := stream.Recv(); err != nil || chunk.Content != "partial" {
		t.Fatalf("chunk=%v err=%v", chunk, err)
	}
	if _, err := stream.Recv(); !errors.Is(err, sentinel) {
		t.Fatalf("stream error=%v", err)
	}
	if attempts != 1 {
		t.Fatalf("stream replayed %d times", attempts)
	}
}

func TestModelRetryNeverRetriesCancellation(t *testing.T) {
	for _, cancellation := range []error{context.Canceled, context.DeadlineExceeded} {
		attempts := 0
		retry := &ModelRetry{MaxAttempts: 3, Retryable: func(error) bool { return true }}
		_, err := retry.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
			attempts++
			return nil, cancellation
		})(context.Background(), nil)
		if !errors.Is(err, cancellation) || attempts != 1 {
			t.Fatalf("err=%v attempts=%d", err, attempts)
		}
	}
}

func TestModelRetryExhaustionPreservesError(t *testing.T) {
	failure := errors.New("provider unavailable")
	for _, retryable := range []bool{true, false} {
		calls := 0
		retry := &ModelRetry{MaxAttempts: 3, Retryable: func(error) bool { return retryable }}
		stream, err := retry.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
			calls++
			return nil, failure
		})(context.Background(), nil)
		want := 1
		if retryable {
			want = 3
		}
		if stream != nil || !errors.Is(err, failure) || calls != want {
			t.Fatalf("retryable=%t stream=%v err=%v calls=%d", retryable, stream, err, calls)
		}
	}
}

func TestModelRetryCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{})
	done := make(chan error, 1)
	calls := 0
	retry := &ModelRetry{MaxAttempts: 3, Delay: time.Hour, Retryable: func(error) bool {
		close(entered)
		return true
	}}
	go func() {
		stream, err := retry.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
			calls++
			return nil, errors.New("temporary failure")
		})(ctx, nil)
		if stream != nil {
			stream.Close()
		}
		done <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("err=%v calls=%d", err, calls)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not release model retry backoff")
	}
}

func TestCircuitBreakerThresholdRestoreAndRunIsolation(t *testing.T) {
	m := &CircuitBreaker{Threshold: 2, Recovery: time.Hour}
	failure := errors.New("provider unavailable")
	calls := 0
	handler := m.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		calls++
		return nil, failure
	})
	for range 2 {
		if _, err := handler(context.Background(), nil); !errors.Is(err, failure) {
			t.Fatal(err)
		}
	}
	if _, err := handler(context.Background(), nil); !errors.Is(err, ErrCircuitOpen) || calls != 2 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	restored := m.NewRun().(*CircuitBreaker)
	if err := restored.UnmarshalRuntimeState(m.MarshalRuntimeState()); err != nil {
		t.Fatal(err)
	}
	if _, err := restored.WrapModel(nil)(context.Background(), nil); !errors.Is(err, ErrCircuitOpen) {
		t.Fatal(err)
	}
	fresh := m.NewRun().(*CircuitBreaker)
	if _, err := fresh.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		return nil, failure
	})(context.Background(), nil); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

func TestCircuitBreakerOnlyOneRecoveryProbe(t *testing.T) {
	m := &CircuitBreaker{Threshold: 1, failures: 1, openUntil: time.Now().Add(-time.Second)}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	handler := m.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		close(entered)
		<-release
		return schema.StreamReaderFromArray([]*schema.Message{{Content: "ok"}}), nil
	})
	go func() {
		stream, err := handler(context.Background(), nil)
		if stream != nil {
			stream.Close()
		}
		done <- err
	}()
	<-entered
	_, err := handler(context.Background(), nil)
	close(release)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	failure := errors.New("probe passed, next provider call")
	if _, err := m.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		return nil, failure
	})(context.Background(), nil); !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

func TestCircuitBreakerCancellationDoesNotCountAsFailure(t *testing.T) {
	m := &CircuitBreaker{Threshold: 1}
	handler := m.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		return nil, context.Canceled
	})
	for range 2 {
		if _, err := handler(context.Background(), nil); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if m.failures != 0 {
		t.Fatal(m.failures)
	}
}
