package thread

import (
	"context"
	"errors"
	"testing"
	"time"

	"eino-cli/deepagent/core/runtime/agentthread"
)

func TestThreadCloseDrainsFinalEventAndClosesOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus := make(chan agentthread.Event, 1)
	core := agentthread.New("thread", &agentthread.RunConfig{}, bus, agentthread.ThreadOptions{})
	adapter, err := NewThread(AdapterConfig{ThreadID: "thread", Thread: core, EventBus: bus})
	if err != nil {
		t.Fatal(err)
	}
	output, err := adapter.Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bus <- agentthread.Event{ThreadID: "thread", RunID: "run", Type: agentthread.EventRunEnd, Payload: agentthread.RunEndPayload{}}
	if err = adapter.Close(ctx); err != nil {
		t.Fatal(err)
	}
	count := 0
	for {
		select {
		case item, ok := <-output.Items:
			if !ok {
				if count != 1 {
					t.Fatalf("final events=%d", count)
				}
				return
			}
			if item.Yield != nil && item.Yield.Reason == "finished" {
				count++
			}
		case <-ctx.Done():
			t.Fatal("output was not closed after Thread.Close")
		}
	}
}

func TestThreadCloseTimeoutCanRetryWhileOutputDrains(t *testing.T) {
	count := threadOutputBridgeBufferSize + 5
	bus := make(chan agentthread.Event, count)
	core := agentthread.New("thread", &agentthread.RunConfig{}, bus, agentthread.ThreadOptions{})
	adapter, err := NewThread(AdapterConfig{ThreadID: "thread", Thread: core, EventBus: bus})
	if err != nil {
		t.Fatal(err)
	}
	output, err := adapter.Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		bus <- agentthread.Event{ThreadID: "thread", RunID: "run", Type: agentthread.EventRunEnd, Payload: agentthread.RunEndPayload{}}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	err = adapter.Close(ctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close ignored blocked output: %v", err)
	}
	drained := make(chan int, 1)
	go func() {
		n := 0
		for range output.Items {
			n++
		}
		drained <- n
	}()
	retry, cancelRetry := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRetry()
	if err = adapter.Close(retry); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-drained:
		if n != count {
			t.Fatalf("lost outputs: %d/%d", n, count)
		}
	case <-retry.Done():
		t.Fatal(retry.Err())
	}
}
