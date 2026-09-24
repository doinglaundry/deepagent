package graph

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
)

func TestStreamReservesExecutionBeforeGoroutineStarts(t *testing.T) {
	for range 20 {
		m := &cancelModel{started: make(chan struct{}), stopped: make(chan struct{})}
		a, err := New(context.Background(), WithModel(m))
		if err != nil {
			t.Fatal(err)
		}
		reader, err := a.Stream(context.Background(), []*schema.Message{schema.UserMessage("first")})
		if err != nil {
			t.Fatal(err)
		}
		second, err := a.Stream(context.Background(), []*schema.Message{schema.UserMessage("second")})
		if err == nil || second != nil {
			t.Fatal("second stream stole ownership")
		}
		if _, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("third")}); err == nil {
			t.Fatal("Run bypassed stream reservation")
		}
		select {
		case <-m.started:
		case <-time.After(time.Second):
			t.Fatal("first stream did not execute")
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := a.Close(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		reader.Close()
		select {
		case <-m.stopped:
		default:
			t.Fatal("Close returned before model cleanup")
		}
		a.mu.Lock()
		pending := a.streamDone != nil || a.streamCancel != nil || a.active
		a.mu.Unlock()
		if pending {
			t.Fatal("Close left stream transport active")
		}
	}
}

func TestStreamEOFMakesNextExecutionAvailable(t *testing.T) {
	for range 20 {
		m := &sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("first", nil)}, {schema.AssistantMessage("second", nil)}}}
		a, err := New(context.Background(), WithModel(m))
		if err != nil {
			t.Fatal(err)
		}
		reader, err := a.Stream(context.Background(), []*schema.Message{schema.UserMessage("go")})
		if err != nil {
			t.Fatal(err)
		}
		for {
			_, err := reader.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		reader.Close()
		out, err := a.Run(context.Background(), []*schema.Message{schema.UserMessage("again")})
		if err != nil || out.Content != "second" {
			t.Fatalf("next run unavailable: out=%v err=%v", out, err)
		}
	}
}

func TestCloseWaitsForPendingStreamTransport(t *testing.T) {
	for range 20 {
		a, err := New(context.Background(), WithModel(&sequenceModel{responses: [][]*schema.Message{{schema.AssistantMessage("done", nil)}}}))
		if err != nil {
			t.Fatal(err)
		}
		reader, err := a.Stream(context.Background(), []*schema.Message{schema.UserMessage("go")})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if err := a.Close(ctx); err != nil {
			cancel()
			t.Fatal(err)
		}
		cancel()
		reader.Close()
		a.mu.Lock()
		pending := a.streamDone != nil || a.active
		a.mu.Unlock()
		if pending {
			t.Fatal("Close returned with a pending stream")
		}
	}
}
