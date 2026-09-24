package agentthread

import (
	"context"
	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/runtime/checkpointer"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"testing"
	"time"
)

type pendingQuestionModel struct {
	resumeModel
	started, release chan struct{}
}

func (m *pendingQuestionModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *pendingQuestionModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.calls == 0 {
		close(m.started)
		select {
		case <-m.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return m.resumeModel.Stream(ctx, input, opts...)
}

type pendingCheckpointStore struct {
	checkpointMemory
	holdOnCompleted  bool
	started, release chan struct{}
	failure          error
}

func (s *pendingCheckpointStore) Set(ctx context.Context, id string, raw []byte) error {
	var envelope checkpointer.Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return err
	}
	var snapshot struct {
		MapValues map[string]struct{ JSONValue types.RunState }
	}
	if err := json.Unmarshal(envelope.EinoSnapshot, &snapshot); err != nil {
		return err
	}
	state := snapshot.MapValues["State"].JSONValue
	hold := string(state.Extensions["pending_inputs"]) == "true"
	if s.holdOnCompleted {
		hold = state.Phase == types.PhaseCompleted
	}
	if hold {
		close(s.started)
		select {
		case <-s.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if s.failure != nil {
			return s.failure
		}
	}
	return s.checkpointMemory.Set(ctx, id, raw)
}
func TestThread_PendingInputCheckpointCommittedBeforeBlocked(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "save failure"}[fail], func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 4*time.Second)
			defer stop()
			store := &pendingCheckpointStore{started: make(chan struct{}), release: make(chan struct{})}
			failure := errors.New("pending checkpoint write failed")
			if fail {
				store.failure = failure
			}
			history := &historyMemory{}
			m := &pendingQuestionModel{started: make(chan struct{}), release: make(chan struct{})}
			cfg := &RunConfig{Agent: graph.Config{Model: m, CheckpointStore: store, Tools: []einotool.BaseTool{tools.GetFollowUpTool()}}}
			events := make(chan Event, 64)
			first := New("thread", cfg, events, ThreadOptions{HistoryStore: history})
			if err := first.Init(ctx); err != nil {
				t.Fatal(err)
			}
			run, err := first.SubmitInput(ctx, schema.UserMessage("ask me"))
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-m.started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			pending, err := first.SubmitInput(ctx, schema.UserMessage("pending question"), WithInputMeta(map[string]string{"MessageID": "pending-id"}))
			if err != nil || pending.RunID != run.RunID {
				t.Fatalf("pending=%+v err=%v", pending, err)
			}
			close(m.release)
			select {
			case <-store.started:
			case <-ctx.Done():
				t.Fatal("pending input not committed to checkpoint")
			}
			if first.ActiveRun() == nil {
				t.Fatal("run inactive before checkpoint persistence")
			}
			for len(events) > 0 {
				e := <-events
				if e.Type == EventFollowUpRequested || e.Type == EventRunEnd {
					t.Fatal("blocked/final published before checkpoint commit")
				}
			}
			close(store.release)
			err = run.RunHandle.Wait(ctx)
			var question FollowUpRequestedPayload
			for len(events) > 0 {
				e := <-events
				if e.Type == EventFollowUpRequested {
					question = e.Payload.(FollowUpRequestedPayload)
				}
			}
			if fail {
				if !errors.Is(err, failure) || question.InterruptID != "" {
					t.Fatalf("failed save published blocked: err=%v question=%+v", err, question)
				}
				got := first.ContextManager().History(ctx)
				if len(got) != 3 || got[2].Content != "pending question" {
					t.Fatalf("failed checkpoint lost accepted input: %v", got)
				}
				return
			}
			if err != nil || question.InterruptID == "" {
				t.Fatalf("missing block: err=%v question=%+v", err, question)
			}
			if len(first.ContextManager().History(ctx)) != 2 {
				t.Fatal("pending input inserted before tool completion")
			}
			restored := New("thread", cfg, make(chan Event, 64), ThreadOptions{HistoryStore: history})
			if err := restored.Init(ctx); err != nil {
				t.Fatal(err)
			}
			resumed, err := restored.ResumeRun(ctx, run.RunID, ResumeRunOptions{CheckpointID: question.CheckpointID, ResumeData: map[string]any{question.InterruptID: &tools.FollowUpInfo{UserAnswer: "yes"}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = resumed.Wait(ctx); err != nil {
				t.Fatal(err)
			}
			if m.calls != 2 || len(m.inputs[1]) != 4 || m.inputs[1][2].Content != "yes" || m.inputs[1][3].Content != "pending question" {
				t.Fatalf("pending input lost or replayed: %v", m.inputs)
			}
			meta := resumed.ConsumedInputsMeta()
			if len(meta) != 2 || meta[1].(map[string]string)["MessageID"] != "pending-id" {
				t.Fatalf("metadata lost: %v", meta)
			}
		})
	}
}
