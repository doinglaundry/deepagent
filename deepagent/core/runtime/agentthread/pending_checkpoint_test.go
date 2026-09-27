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
	"github.com/cloudwego/eino/schema"
	"sync"
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
	gateOnce         sync.Once
	gateErr          error
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
		s.gateOnce.Do(func() {
			close(s.started)
			select {
			case <-s.release:
			case <-ctx.Done():
				s.gateErr = ctx.Err()
			}
		})
		if s.gateErr != nil {
			return s.gateErr
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
			cfg := &RunConfig{Agent: graph.Config{Model: m, CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: tools.GetFollowUpTool()}}}}
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

type pendingSaveStore struct {
	historyMemory
	started chan struct{}
	release chan struct{}
	failure error
}

func (s *pendingSaveStore) Append(ctx context.Context, r *HistoryRecord) error {
	if r.Message.Content == "accepted pending" {
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
	return s.historyMemory.Append(ctx, r)
}
func TestThread_CancelPersistsAcceptedPendingBeforeFinalEventAndWait(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved", true: "save failure"}[fail], func(t *testing.T) {
			ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			failure := errors.New("history unavailable")
			store := &pendingSaveStore{started: make(chan struct{}), release: make(chan struct{})}
			if fail {
				store.failure = failure
			}
			model := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
			events := make(chan Event, 32)
			thread := New("thread", &RunConfig{Agent: graph.Config{Model: model}}, events, ThreadOptions{HistoryStore: store})
			if err := thread.Init(ctx); err != nil {
				t.Fatal(err)
			}
			first, err := thread.SubmitInput(runCtx, schema.UserMessage("first"))
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-model.started:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			next, err := thread.SubmitInput(runCtx, schema.UserMessage("accepted pending"), WithInputMeta(map[string]string{"message_id": "pending-id"}))
			if err != nil || next.RunID != first.RunID {
				t.Fatalf("next=%+v err=%v", next, err)
			}
			cancel()
			select {
			case <-store.started:
			case <-ctx.Done():
				t.Fatal("accepted pending input never persisted")
			}
			if thread.ActiveRun() == nil {
				t.Fatal("run inactive before pending input persisted")
			}
			short, end := context.WithTimeout(ctx, 10*time.Millisecond)
			if err := first.RunHandle.Wait(short); !errors.Is(err, context.DeadlineExceeded) {
				end()
				t.Fatalf("Wait returned before persistence: %v", err)
			}
			end()
			for len(events) > 0 {
				if e := <-events; e.Type == EventRunEnd {
					t.Fatal("terminal event overtook history persistence")
				}
			}
			close(store.release)
			err = first.RunHandle.Wait(ctx)
			if !errors.Is(err, context.Canceled) || (fail && !errors.Is(err, failure)) {
				t.Fatalf("lost failure cause: %v", err)
			}
			history := thread.ContextManager().History(ctx)
			want := 2
			if fail {
				want = 1
			}
			if len(history) != want || len(store.records) != want {
				t.Fatalf("history=%v records=%d", history, len(store.records))
			}
			inputs := next.RunHandle.ConsumedInputs()
			if len(inputs) != 2 || inputs[1].Content != "accepted pending" || len(next.RunHandle.ConsumedInputsMeta()) != 2 {
				t.Fatal("accepted input ownership lost")
			}
			metadata, ok := next.RunHandle.ConsumedInputsMeta()[1].(map[string]string)
			if !ok || metadata["message_id"] != "pending-id" {
				t.Fatal("pending metadata changed")
			}
			if !fail && store.records[1].RunID != first.RunID {
				t.Fatal("pending history changed run ownership")
			}
			if err := thread.Close(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
}
