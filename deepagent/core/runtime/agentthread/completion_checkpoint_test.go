package agentthread

import (
	"context"
	"errors"
	"testing"
	"time"

	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/tools"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
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
			cfg := &RunConfig{Agent: graph.Config{Model: &resumeModel{}, CheckpointStore: store, Tools: []einotool.BaseTool{tools.GetFollowUpTool()}}}
			history := &historyMemory{}
			events := make(chan Event, 64)
			first := New("thread", cfg, events, ThreadOptions{HistoryStore: history})
			if err := first.Init(ctx); err != nil {
				t.Fatal(err)
			}
			started, err := first.SubmitInput(ctx, schema.UserMessage("ask"))
			if err != nil {
				t.Fatal(err)
			}
			if err = started.RunHandle.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			var question FollowUpRequestedPayload
			for len(events) > 0 {
				e := <-events
				if e.Type == EventFollowUpRequested {
					question = e.Payload.(FollowUpRequestedPayload)
				}
			}
			next := New("thread", cfg, events, ThreadOptions{HistoryStore: history})
			if err = next.Init(ctx); err != nil {
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
			if next.ActiveRun() == nil {
				t.Fatal("inactive before checkpoint commit")
			}
			waitCtx, stop := context.WithTimeout(ctx, 10*time.Millisecond)
			if err := handle.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("Wait overtook save: %v", err)
			}
			stop()
			for len(events) > 0 {
				if e := <-events; e.Type == EventRunEnd {
					t.Fatal("final event overtook save")
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
