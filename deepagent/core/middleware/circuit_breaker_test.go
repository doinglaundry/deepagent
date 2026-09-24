package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

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
