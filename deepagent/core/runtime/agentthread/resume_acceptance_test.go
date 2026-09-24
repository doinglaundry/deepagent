package agentthread

import (
	"context"
	"errors"
	"testing"

	"eino-cli/deepagent/core/graph"
	"github.com/cloudwego/eino/compose"
)

type unreadableCheckpoint struct {
	compose.CheckPointStore
	err error
}

func (s unreadableCheckpoint) Get(context.Context, string) ([]byte, bool, error) {
	return nil, false, s.err
}

func TestThread_ResumeRejectsUnavailableCheckpointBeforeAcceptance(t *testing.T) {
	failure := errors.New("store unavailable")
	for _, tc := range []struct {
		name  string
		store compose.CheckPointStore
		cause error
	}{
		{name: "unconfigured"},
		{name: "missing", store: &checkpointMemory{}},
		{name: "read failure", store: unreadableCheckpoint{err: failure}, cause: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			events := make(chan Event, 8)
			model := &threadModel{}
			thread := New("thread", &RunConfig{Agent: graph.Config{Model: model, CheckpointStore: tc.store}}, events, ThreadOptions{})
			if err := thread.Init(ctx); err != nil {
				t.Fatal(err)
			}
			defer thread.Close(ctx)
			hookCalled := false
			handle, err := thread.ResumeRun(ctx, "run", ResumeRunOptions{CheckpointID: "missing", OnRunStart: func(ctx context.Context, _ RunStartRequest) context.Context { hookCalled = true; return ctx }})
			if err == nil || handle != nil {
				t.Fatalf("resume accepted: handle=%v err=%v", handle, err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("lost cause: %v", err)
			}
			if hookCalled || thread.ActiveRun() != nil || len(events) != 0 || model.calls != 0 {
				t.Fatal("failed resume started execution or published acceptance")
			}
		})
	}
}
