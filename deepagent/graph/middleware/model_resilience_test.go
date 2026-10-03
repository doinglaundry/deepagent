package middleware

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestModelRetryOnlyRetriesBeforeStream(t *testing.T) {
	modelErr := errors.New("temporary model failure")
	modelRetry := &ModelRetry{MaxAttempts: 3, Retryable: func(err error) bool { return errors.Is(err, modelErr) }}
	attemptCount := 0
	modelHandler := modelRetry.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		attemptCount++
		if attemptCount == 1 {
			return nil, modelErr
		}
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("done", nil)}), nil
	})
	stream, err := modelHandler(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
	if attemptCount != 2 {
		t.Fatalf("attempts=%d", attemptCount)
	}
	attemptCount = 0
	modelHandler = modelRetry.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		attemptCount++
		reader, writer := schema.Pipe[*schema.Message](2)
		writer.Send(schema.AssistantMessage("partial", nil), nil)
		writer.Send(nil, modelErr)
		writer.Close()
		return reader, nil
	})
	stream, err = modelHandler(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	chunk, streamRecvErr := stream.Recv()
	if streamRecvErr != nil || chunk.Content != "partial" {
		t.Fatalf("chunk=%v err=%v", chunk, streamRecvErr)
	}
	_, recvErr := stream.Recv()
	if !errors.Is(recvErr, modelErr) {
		t.Fatalf("stream error=%v", recvErr)
	}
	if attemptCount != 1 {
		t.Fatalf("stream replayed %d times", attemptCount)
	}
}

func TestModelRetryNeverRetriesCancellation(t *testing.T) {
	for _, cancellation := range []error{context.Canceled, context.DeadlineExceeded} {
		attemptCount := 0
		modelRetry := &ModelRetry{MaxAttempts: 3, Retryable: func(error) bool { return true }}
		_, err := modelRetry.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
			attemptCount++
			return nil, cancellation
		})(context.Background(), nil)
		if !errors.Is(err, cancellation) || attemptCount != 1 {
			t.Fatalf("err=%v attempts=%d", err, attemptCount)
		}
	}
}

func TestModelRetryExhaustionPreservesError(t *testing.T) {
	providerErr := errors.New("provider unavailable")
	for _, isRetryable := range []bool{true, false} {
		attemptCount := 0
		modelRetry := &ModelRetry{MaxAttempts: 3, Retryable: func(error) bool { return isRetryable }}
		stream, err := modelRetry.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
			attemptCount++
			return nil, providerErr
		})(context.Background(), nil)
		expectedAttempts := 1
		if isRetryable {
			expectedAttempts = 3
		}
		if stream != nil || !errors.Is(err, providerErr) || attemptCount != expectedAttempts {
			t.Fatalf("retryable=%t stream=%v err=%v calls=%d", isRetryable, stream, err, attemptCount)
		}
	}
}

func TestModelRetryCancellationDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backoffEntered := make(chan struct{})
	retryDone := make(chan error, 1)
	attemptCount := 0
	modelRetry := &ModelRetry{MaxAttempts: 3, Delay: time.Hour, Retryable: func(error) bool {
		close(backoffEntered)
		return true
	}}
	go func() {
		stream, err := modelRetry.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
			attemptCount++
			return nil, errors.New("temporary failure")
		})(ctx, nil)
		if stream != nil {
			stream.Close()
		}
		retryDone <- err
	}()
	<-backoffEntered
	cancel()
	select {
	case err := <-retryDone:
		if !errors.Is(err, context.Canceled) || attemptCount != 1 {
			t.Fatalf("err=%v calls=%d", err, attemptCount)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancellation did not release model retry backoff")
	}
}

func TestCircuitBreakerThresholdRestoreAndRunIsolation(t *testing.T) {
	circuitBreaker := &CircuitBreaker{Threshold: 2, Recovery: time.Hour}
	providerErr := errors.New("provider unavailable")
	attemptCount := 0
	modelHandler := circuitBreaker.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		attemptCount++
		return nil, providerErr
	})
	for range 2 {
		_, err := modelHandler(context.Background(), nil)
		if !errors.Is(err, providerErr) {
			t.Fatal(err)
		}
	}
	_, handlerErr := modelHandler(context.Background(), nil)
	if !errors.Is(handlerErr, ErrCircuitOpen) || attemptCount != 2 {
		t.Fatalf("calls=%d err=%v", attemptCount, handlerErr)
	}
	restoredBreaker := circuitBreaker.NewRun().(*CircuitBreaker)
	restoreErr := restoredBreaker.UnmarshalRuntimeState(circuitBreaker.MarshalRuntimeState())
	if restoreErr != nil {
		t.Fatal(restoreErr)
	}
	_, restoredCallErr := restoredBreaker.WrapModel(nil)(context.Background(), nil)
	if !errors.Is(restoredCallErr, ErrCircuitOpen) {
		t.Fatal(restoredCallErr)
	}
	freshBreaker := circuitBreaker.NewRun().(*CircuitBreaker)
	_, freshCallErr := freshBreaker.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		return nil, providerErr
	})(context.Background(), nil)
	if !errors.Is(freshCallErr, providerErr) {
		t.Fatal(freshCallErr)
	}
}

func TestCircuitBreakerOnlyOneRecoveryProbe(t *testing.T) {
	circuitBreaker := &CircuitBreaker{Threshold: 1, failures: 1, openUntil: time.Now().Add(-time.Second)}
	probeStarted, releaseProbe, probeDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	modelHandler := circuitBreaker.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		close(probeStarted)
		<-releaseProbe
		return schema.StreamReaderFromArray([]*schema.Message{{Content: "ok"}}), nil
	})
	go func() {
		stream, err := modelHandler(context.Background(), nil)
		if stream != nil {
			stream.Close()
		}
		probeDone <- err
	}()
	<-probeStarted
	_, err := modelHandler(context.Background(), nil)
	close(releaseProbe)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatal(err)
	}
	probeErr := <-probeDone
	if probeErr != nil {
		t.Fatal(probeErr)
	}
	providerErr := errors.New("probe passed, next provider call")
	_, nextCallErr := circuitBreaker.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		return nil, providerErr
	})(context.Background(), nil)
	if !errors.Is(nextCallErr, providerErr) {
		t.Fatal(nextCallErr)
	}
}

func TestCircuitBreakerCancellationDoesNotCountAsFailure(t *testing.T) {
	circuitBreaker := &CircuitBreaker{Threshold: 1}
	modelHandler := circuitBreaker.WrapModel(func(context.Context, []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		return nil, context.Canceled
	})
	for range 2 {
		_, err := modelHandler(context.Background(), nil)
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if circuitBreaker.failures != 0 {
		t.Fatal(circuitBreaker.failures)
	}
}
