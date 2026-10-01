package deepagents

import (
	"context"

	"eino-cli/deepagent/core/tools"
	"errors"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"testing"
	"time"
)

func TestThread_CompletionCheckpointPersistsBeforeFinalEvent(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "save failure"}[fail], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			store := &pendingCheckpointStore{started: make(chan struct{}), release: make(chan struct{}), holdOnCompleted: true}
			failure := errors.New("terminal snapshot write failed")
			if fail {
				store.failure = failure
			}
			cfg := &RunConfig{Agent: Config{Model: &resumeModel{}, CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: tools.GetFollowUpTool()}}}}
			history := &historyMemory{}
			events := make(chan Event, 64)
			first := newTestThread("thread", cfg, events, ThreadOptions{HistoryStore: history})
			{
				err := first.InitHistory(ctx)
				if err != nil {
					t.Fatal(err)
				}
			}
			started, err := first.SubmitInput(ctx, schema.UserMessage("ask"))
			if err != nil {
				t.Fatal(err)
			}
			err = started.RunHandle.Wait(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var question FollowUpRequestedPayload
			for len(events) > 0 {
				e := <-events
				if e.Type == EventFollowUpRequested {
					question = e.Payload.(FollowUpRequestedPayload)
				}
			}
			next := newTestThread("thread", cfg, events, ThreadOptions{HistoryStore: history})
			err = next.InitHistory(ctx)
			if err != nil {
				t.Fatal(err)
			}
			handle, err := next.ResumeRun(ctx, started.RunID, ResumeRunOptions{CheckpointID: question.CheckpointID, ResumeData: map[string]any{question.InterruptID: &tools.FollowUpInfo{UserAnswer: "a"}}})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-store.started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if next.CurrentRun() == nil {
				t.Fatal("inactive before checkpoint commit")
			}
			waitCtx, stop := context.WithTimeout(ctx, 10*time.Millisecond)
			{
				err := handle.Wait(waitCtx)
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("Wait overtook save: %v", err)
				}
			}
			stop()
			for len(events) > 0 {
				{
					e := <-events
					if e.Type == EventRunEnd {
						t.Fatal("final event overtook save")
					}
				}
			}
			close(store.release)
			err = handle.Wait(ctx)
			if fail && !errors.Is(err, failure) {
				t.Fatalf("save error hidden: %v", err)
			}
			if !fail && err != nil {
				t.Fatal(err)
			}
			sawError := false
			for len(events) > 0 {
				e := <-events
				if e.Type == EventError {
					sawError = true
				}
				if e.Type == EventFollowUpRequested {
					t.Fatal("terminal save failure republished blocked")
				}
			}
			if sawError != fail {
				t.Fatalf("failure event=%v want=%v", sawError, fail)
			}
		})
	}
}

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
		{name: "missing", store: &threadCheckpointMemory{}},
		{name: "read failure", store: unreadableCheckpoint{err: failure}, cause: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			events := make(chan Event, 8)
			model := &threadModel{}
			thread := newTestThread("thread", &RunConfig{Agent: Config{Model: model, CheckpointStore: tc.store}}, events, ThreadOptions{})
			{
				err := thread.InitHistory(ctx)
				if err != nil {
					t.Fatal(err)
				}
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
			if hookCalled || thread.CurrentRun() != nil || len(events) != 0 || model.calls != 0 {
				t.Fatal("failed resume started execution or published acceptance")
			}
		})
	}
}
