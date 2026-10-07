package thread

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	checkpointer "eino-cli/deepagent/graph/checkpoint"
	"eino-cli/deepagent/graph/execution"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	inputpkg "eino-cli/deepagent/protocol/input"
	runpkg "eino-cli/deepagent/run"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestThread_ApprovalCancellationRestoresInputOwnershipWithoutReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	model := &legacyParityScriptedModel{stream: func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message] {
		return legacyParityMessageStream(schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "echo", Arguments: `{}`}}}))
	}}
	store := &threadCheckpointMemory{}
	cfg := &runpkg.Config{Graph: execution.Config{Model: model, CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{{Tool: legacyParityEchoTool{}, RequiresApproval: true}}}}
	events := make(chan runpkg.Event, 32)
	first := newTestThread("1", cfg, events, ThreadOptions{})
	defer first.Close(ctx)
	message := schema.UserMessage("execute once")
	attachAttribute(message, MessageAttribute{MessageID: "101"})
	started, err := first.SubmitInput(ctx, message, WithMessageID("101"))
	if err != nil {
		t.Fatal(err)
	}
	err = started.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var approval runpkg.ApprovalRequiredPayload
	for len(events) > 0 {
		event := <-events
		if event.Type == runpkg.EventApproveRequested {
			approval = event.Payload.(runpkg.ApprovalRequiredPayload)
		}
	}
	restored := newTestThread("1", cfg, nil, ThreadOptions{})
	defer restored.Close(ctx)
	output, err := restored.Init(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(inputpkg.ResumeRunPayload{RunID: started.RunID, CheckpointID: approval.CheckpointID, InterruptID: approval.InterruptID, Approval: &inputpkg.ApprovalDecision{CancelRun: true}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = restored.PostMessage(ctx, &TransportMessage{ID: "102", Type: MessageTypeResumeRun, Payload: raw})
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case item := <-output.Items:
			if item.Yield == nil {
				continue
			}
			if item.Yield.Reason != "interrupted" || item.Event == nil || item.Event.RunID != started.RunID {
				t.Fatalf("wrong canceled Run: %+v", item)
			}
			repeated, err := restored.SubmitInput(ctx, message, WithMessageID("101"))
			if err != nil {
				t.Fatal(err)
			}
			if repeated.RunID != started.RunID || repeated.Started {
				t.Fatalf("canceled input replayed in a new Run: %+v", repeated)
			}
			err = repeated.RunHandle.Wait(ctx)
			if !errors.Is(err, context.Canceled) || len(model.inputs) != 1 {
				t.Fatalf("cancel err=%v model calls=%d", err, len(model.inputs))
			}
			return
		case <-ctx.Done():
			t.Fatal("missing canceled terminal event")
		}
	}
}

func TestThread_UnknownToolOutcomeEndsOriginalRunWithoutReplayingInput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	input := types.Input{MessageID: "2000000000000000664", Message: schema.UserMessage("write once"), Meta: map[string]string{"sender": "user"}}
	state := &types.RunState{
		Version: 1, ThreadID: "thread", RunID: "run", Phase: types.PhaseBlocked,
		Consumed: []types.Input{input}, PreparedInputs: 1,
		Calls: []types.ToolCallState{{Call: types.ToolCall{ID: "write", Name: "write_file", Arguments: `{}`}, Status: types.CallOutcomeUnknown}},
	}
	snapshot, err := json.Marshal(map[string]any{"MapValues": map[string]any{"State": map[string]any{
		"Type": map[string]string{"SimpleType": "deepagent_run_state_v1"}, "JSONValue": state,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(checkpointer.Envelope{Version: 1, ThreadID: "thread", RunID: "run", GraphVersion: "core-graph-v1", EinoSnapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	store := &threadCheckpointMemory{values: map[string][]byte{"checkpoint": raw}}
	model := &threadModel{}
	events := make(chan runpkg.Event, 32)
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: model, CheckpointStore: store}}, events, ThreadOptions{})
	defer thread.Close(ctx)
	handle, err := thread.ResumeRun(ctx, "run", ResumeRunOptions{CheckpointID: "checkpoint"})
	if err != nil {
		t.Fatalf("unknown outcome must publish a failed Run: %v", err)
	}
	err = handle.Wait(ctx)
	if err == nil || !strings.Contains(err.Error(), "unknown outcome") {
		t.Fatalf("lost uncertain outcome: %v", err)
	}
	failed := false
	for len(events) > 0 {
		event := <-events
		if event.Type == runpkg.EventRunEnd {
			end := event.Payload.(runpkg.RunEndPayload)
			failed = event.RunID == "run" && end.Status == "failed" && len(event.ConsumedInputs) == 1 && event.ConsumedInputs[0].Content == "write once"
		}
	}
	if !failed || thread.CurrentRun() != nil {
		t.Fatal("original Run never reached its failed terminal boundary")
	}
	metadata := handle.ConsumedInputsMeta()
	if len(metadata) != 1 || metadata[0].(map[string]string)["sender"] != "user" {
		t.Fatalf("lost consumed input metadata: %v", metadata)
	}
	repeated, err := thread.SubmitInput(ctx, input.Message, WithMessageID(input.MessageID))
	if err != nil {
		t.Fatal(err)
	}
	if repeated.RunID != "run" || repeated.Started || model.calls != 0 {
		t.Fatalf("unsafe replay: input=%+v model calls=%d", repeated, model.calls)
	}
}

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
			cfg := &runpkg.Config{Graph: execution.Config{Model: &resumeModel{}, CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{tools.NewFollowUpTool()}}}
			history := &historyMemory{}
			events := make(chan runpkg.Event, 64)
			first := newTestThread("thread", cfg, events, ThreadOptions{ConversationRepository: history})
			initHistoryErr := first.InitHistory(ctx)
			if initHistoryErr != nil {
				t.Fatal(initHistoryErr)
			}
			started, err := first.SubmitInput(ctx, schema.UserMessage("ask"))
			if err != nil {
				t.Fatal(err)
			}
			err = started.RunHandle.Wait(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var question runpkg.FollowUpRequestedPayload
			for len(events) > 0 {
				e := <-events
				if e.Type == runpkg.EventFollowUpRequested {
					question = e.Payload.(runpkg.FollowUpRequestedPayload)
				}
			}
			next := newTestThread("thread", cfg, events, ThreadOptions{ConversationRepository: history})
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
			waitErr := handle.Wait(waitCtx)
			if !errors.Is(waitErr, context.DeadlineExceeded) {
				t.Fatalf("Wait overtook save: %v", waitErr)
			}
			stop()
			for len(events) > 0 {
				e := <-events
				if e.Type == runpkg.EventRunEnd {
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
				if e.Type == runpkg.EventError {
					sawError = true
				}
				if e.Type == runpkg.EventFollowUpRequested {
					t.Fatal("terminal save failure republished blocked")
				}
			}
			if sawError != fail {
				t.Fatalf("failure event=%v want=%v", sawError, fail)
			}
		})
	}
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
			events := make(chan runpkg.Event, 8)
			model := &threadModel{}
			thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: model, CheckpointStore: tc.store}}, events, ThreadOptions{})
			initHistoryErr := thread.InitHistory(ctx)
			if initHistoryErr != nil {
				t.Fatal(initHistoryErr)
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

func TestThread_PendingInputCheckpointCommittedBeforeBlocked(t *testing.T) {
	for _, scenario := range []string{"resume", "save failure", "save timeout"} {
		t.Run(scenario, func(t *testing.T) {
			fail := scenario != "resume"
			ctx, stop := context.WithTimeout(context.Background(), 15*time.Second)
			defer stop()
			store := &pendingCheckpointStore{started: make(chan struct{}), release: make(chan struct{})}
			failure := errors.New("pending checkpoint write failed")
			if fail {
				store.failure = failure
			}
			history := &pendingConversationRepository{}
			m := &pendingQuestionModel{started: make(chan struct{}), release: make(chan struct{})}
			cfg := &runpkg.Config{Graph: execution.Config{Model: m, CheckpointStore: store, ToolDescriptors: []tools.ToolDescriptor{tools.NewFollowUpTool()}}}
			events := make(chan runpkg.Event, 64)
			first := newTestThread("thread", cfg, events, ThreadOptions{ConversationRepository: history})
			firstInitHistoryErr := first.InitHistory(ctx)
			if firstInitHistoryErr != nil {
				t.Fatal(firstInitHistoryErr)
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
			if first.CurrentRun() == nil {
				t.Fatal("run inactive before checkpoint persistence")
			}
			for len(events) > 0 {
				e := <-events
				if e.Type == runpkg.EventFollowUpRequested || e.Type == runpkg.EventRunEnd {
					t.Fatal("blocked/final published before checkpoint commit")
				}
			}
			if scenario != "save timeout" {
				close(store.release)
			}
			err = run.RunHandle.Wait(ctx)
			var question runpkg.FollowUpRequestedPayload
			for len(events) > 0 {
				e := <-events
				if e.Type == runpkg.EventFollowUpRequested {
					question = e.Payload.(runpkg.FollowUpRequestedPayload)
				}
			}
			if fail {
				expected := failure
				if scenario == "save timeout" {
					expected = context.DeadlineExceeded
				}
				if !errors.Is(err, expected) || question.InterruptID != "" {
					t.Fatalf("failed save published blocked: err=%v question=%+v", err, question)
				}
				got := first.ContextManager().GetHistory(ctx)
				if len(got) != 3 || got[2].Content != "pending question" {
					t.Fatalf("failed checkpoint lost accepted input: %v", got)
				}
				return
			}
			if err != nil || question.InterruptID == "" {
				t.Fatalf("missing block: err=%v question=%+v", err, question)
			}
			if len(first.ContextManager().GetHistory(ctx)) != 2 {
				t.Fatal("pending input inserted before tool completion")
			}
			restored := newTestThread("thread", cfg, make(chan runpkg.Event, 64), ThreadOptions{ConversationRepository: history})
			initHistoryErr := restored.InitHistory(ctx)
			if initHistoryErr != nil {
				t.Fatal(initHistoryErr)
			}
			resumed, err := restored.ResumeRun(ctx, run.RunID, ResumeRunOptions{CheckpointID: question.CheckpointID, ResumeData: map[string]any{question.InterruptID: &tools.FollowUpInfo{UserAnswer: "yes"}}})
			if err != nil {
				t.Fatal(err)
			}
			err = resumed.Wait(ctx)
			if err != nil {
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
			events := make(chan runpkg.Event, 32)
			thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: model}}, events, ThreadOptions{ConversationRepository: store})
			initHistoryErr := thread.InitHistory(ctx)
			if initHistoryErr != nil {
				t.Fatal(initHistoryErr)
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
			if thread.CurrentRun() == nil {
				t.Fatal("run inactive before pending input persisted")
			}
			short, end := context.WithTimeout(ctx, 10*time.Millisecond)
			waitErr := first.RunHandle.Wait(short)
			if !errors.Is(waitErr, context.DeadlineExceeded) {
				end()
				t.Fatalf("Wait returned before persistence: %v", waitErr)
			}
			end()
			for len(events) > 0 {
				e := <-events
				if e.Type == runpkg.EventRunEnd {
					t.Fatal("terminal event overtook history persistence")
				}
			}
			close(store.release)
			err = first.RunHandle.Wait(ctx)
			if !errors.Is(err, context.Canceled) || (fail && !errors.Is(err, failure)) {
				t.Fatalf("lost failure cause: %v", err)
			}
			history := thread.ContextManager().GetHistory(ctx)
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
			closeErr := thread.Close(ctx)
			if closeErr != nil {
				t.Fatal(closeErr)
			}
		})
	}
}
