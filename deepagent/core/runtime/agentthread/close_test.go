package agentthread

import (
	"context"
	"testing"
	"time"

	"eino-cli/deepagent/core/graph"
	"github.com/cloudwego/eino/schema"
)

func TestThread_CloseCancelsActiveRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
	thread := New("thread", &RunConfig{Agent: graph.Config{Model: m}}, make(chan Event, 16), ThreadOptions{})
	accepted, err := thread.SubmitInput(ctx, schema.UserMessage("run"))
	if err != nil {
		t.Fatal(err)
	}
	<-m.started
	if err := thread.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if thread.ActiveRun() != nil || accepted.RunHandle.IsActive() {
		t.Fatal("close returned with active run")
	}
	if _, err := thread.SubmitInput(ctx, schema.UserMessage("late")); err == nil {
		t.Fatal("closed thread accepted input")
	}
	if err := thread.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
