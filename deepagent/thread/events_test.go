package thread

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"eino-cli/deepagent/graph/execution"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	eventpkg "eino-cli/deepagent/protocol/event"
	inputpkg "eino-cli/deepagent/protocol/input"
	runpkg "eino-cli/deepagent/run"

	"github.com/cloudwego/eino/schema"
)

func TestThreadAdapter_InputConsumedPreservesIndividualIdentityAndMedia(t *testing.T) {
	first, second := schema.UserMessage("first"), schema.UserMessage("describe")
	attachAttribute(first, MessageAttribute{MessageID: "one"})
	attachAttribute(second, MessageAttribute{MessageID: "two", SenderID: "person", SenderType: "user"})
	url := "https://example.test/image.png"
	second.UserInputMultiContent = []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeText, Text: "describe"}, {Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url, MIMEType: "image/png"}}}}
	kind, payload, err := agentEventPayloadForOutput(runpkg.Event{Type: runpkg.EventInputConsumed, Payload: types.Input{Message: second}, ConsumedInputs: []*schema.Message{first, second}}, nil)
	if err != nil || kind != eventpkg.EventTypeInputConsumed {
		t.Fatalf("kind=%s err=%v", kind, err)
	}
	message := payload.(*eventpkg.MessageEventPayload)
	if message.MessageID == nil || *message.MessageID != "two" || message.Sender == nil || message.Sender.SenderID != "person" {
		t.Fatalf("identity=%+v", message)
	}
	if len(message.Parts) != 2 || message.Parts[1].URL != url || message.Parts[1].MIMEType != "image/png" {
		t.Fatalf("media lost: %+v", message.Parts)
	}
	if !reflect.DeepEqual(message.ConsumedMessageIDs, []string{"one", "two"}) {
		t.Fatal("run ownership metadata lost")
	}
}

func TestThreadAdapter_FollowUpIncludesQuestion(t *testing.T) {
	payload := followUpRequiredPayload(runpkg.FollowUpRequestedPayload{
		InterruptID: "interrupt-1", CheckpointID: "checkpoint-1",
		Info: &tools.FollowUpInfo{Question: "选择哪个目录？", Questions: []string{"src", "docs"}},
	})
	var info struct {
		Question  string   `json:"question"`
		Questions []string `json:"questions"`
	}
	err := json.Unmarshal(payload.Info, &info)
	if err != nil {
		t.Fatal(err)
	}
	if info.Question != "选择哪个目录？" || !reflect.DeepEqual(info.Questions, []string{"src", "docs"}) {
		t.Fatalf("follow-up lost question or choices: %s", payload.Info)
	}
	if payload.InterruptID != "interrupt-1" || payload.CheckpointID != "checkpoint-1" {
		t.Fatalf("follow-up lost resume identity: %+v", payload)
	}
}

func TestThreadAdapter_ApprovalPreservesCallIdentity(t *testing.T) {
	payload := convertApprovalRequiredPayload(runpkg.ApprovalRequiredPayload{InterruptID: "interrupt", CheckpointID: "checkpoint", ApprovalInfo: &tools.ApprovalInfo{CallID: "call", ToolName: "execute", Arguments: `{"command":"pwd"}`}})
	if payload.ToolCallID != "call" || payload.InterruptID != "interrupt" || payload.CheckpointID != "checkpoint" || payload.ToolName != "execute" || payload.ArgumentsJSON == nil {
		t.Fatalf("approval identity lost: %+v", payload)
	}
}

func TestThreadAdapter_RunEndPreservesOutcome(t *testing.T) {
	for _, status := range []string{"finished", "blocked", "interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			end := runpkg.RunEndPayload{Status: status}
			if status == "blocked" {
				end.CheckpointID = "checkpoint"
				end.InterruptID = "interrupt"
			}
			kind, value, err := agentEventPayloadForOutput(runpkg.Event{
				Type: runpkg.EventRunEnd, RunID: "run", Payload: end,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if kind != eventpkg.EventTypeRunStatus {
				t.Fatalf("terminal outcome lost: kind=%q status=%q", kind, status)
			}
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var payload struct {
				Status       string `json:"status"`
				CheckpointID string `json:"checkpoint_id"`
				InterruptID  string `json:"interrupt_id"`
			}
			err = json.Unmarshal(raw, &payload)
			if err != nil {
				t.Fatal(err)
			}
			if payload.Status != status || payload.CheckpointID != end.CheckpointID || payload.InterruptID != end.InterruptID {
				t.Fatalf("outcome=%s want=%+v", raw, end)
			}
		})
	}
}

func TestInterruptBatchPreservesEveryApproval(t *testing.T) {
	batch := runpkg.InterruptBatchPayload{CheckpointID: "checkpoint", Items: []runpkg.InterruptBatchItem{
		{InterruptID: "first", Kind: runpkg.InterruptItemApprove, ApprovalInfo: &tools.ApprovalInfo{CallID: "call-a", ToolName: "write_file"}},
		{InterruptID: "second", Kind: runpkg.InterruptItemApprove, ApprovalInfo: &tools.ApprovalInfo{CallID: "call-b", ToolName: "execute"}},
	}}
	kind, payload, err := agentEventPayloadForOutput(runpkg.Event{Type: runpkg.EventInterruptBatchRequested, Payload: batch}, nil)
	if err != nil || kind != eventpkg.EventTypeInputRequired {
		t.Fatalf("kind=%s payload=%v err=%v", kind, payload, err)
	}
	out, ok := payload.(*eventpkg.InterruptBatchRequiredEventPayload)
	if !ok || len(out.Items) != 2 || out.Items[0].InterruptID != "first" || out.Items[1].InterruptID != "second" {
		t.Fatalf("payload=%+v", payload)
	}
	resume := inputpkg.ResumeRunPayload{InterruptID: "first", Answers: []inputpkg.ResumeAnswer{
		{InterruptID: "first", Approval: &inputpkg.ApprovalDecision{Approved: true}},
		{InterruptID: "second", Approval: &inputpkg.ApprovalDecision{Approved: false}},
	}}
	answers, err := resumeData(context.Background(), resume, nil)
	if err != nil || len(answers) != 2 {
		t.Fatalf("answers=%v err=%v", answers, err)
	}
	if answers["first"].(*tools.ApprovalResult).Approved != true || answers["second"].(*tools.ApprovalResult).Approved != false {
		t.Fatalf("answers=%v", answers)
	}
}

func TestThreadExternalInterruptTimeoutRetainsMetadata(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	m := &legacyParityScriptedModel{stream: func(ctx context.Context, _ int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		reader, writer := schema.Pipe[*schema.Message](0)
		go func() { defer writer.Close(); close(started); <-ctx.Done() }()
		return reader
	}}
	events := make(chan runpkg.Event, 32)
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: m, CheckpointStore: &legacyParityMemoryCheckpoints{}}}, events, ThreadOptions{})
	accepted, err := thread.SubmitInput(ctx, schema.UserMessage("wait"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	timeout := 10 * time.Millisecond
	thread.InterruptRun(runpkg.InterruptOptions{Timeout: &timeout, Metadata: map[string]string{"reason": "handoff"}})
	thread.InterruptRun(runpkg.InterruptOptions{Timeout: &timeout, Metadata: map[string]string{"reason": "handoff"}})
	waitErr := accepted.RunHandle.Wait(ctx)
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	found := false
	for len(events) > 0 {
		event := <-events
		if event.Type == runpkg.EventError {
			t.Fatalf("external timeout became error: %+v", event)
		}
		if event.Type == runpkg.EventInterrupted {
			p := event.Payload.(runpkg.InterruptedPayload)
			if p.Source != "external" || p.Metadata["reason"] != "handoff" || p.TimeoutMS != 10 {
				t.Fatalf("lost correlation: %+v", p)
			}
			found = true
		}
	}
	if !found || thread.CurrentRun() != nil {
		t.Fatal("missing terminal interrupt or active run retained")
	}
}

func TestThreadCloseDrainsFinalEventAndClosesOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	bus := make(chan runpkg.Event, 1)
	core := newTestThread("thread", &runpkg.Config{}, bus, ThreadOptions{})
	adapter := core
	output, err := adapter.Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bus <- runpkg.Event{ThreadID: "thread", RunID: "run", Type: runpkg.EventRunEnd, Payload: runpkg.RunEndPayload{}}
	err = adapter.Close(ctx)
	if err != nil {
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
	bus := make(chan runpkg.Event, count)
	core := newTestThread("thread", &runpkg.Config{}, bus, ThreadOptions{})
	adapter := core
	output, err := adapter.Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		bus <- runpkg.Event{ThreadID: "thread", RunID: "run", Type: runpkg.EventRunEnd, Payload: runpkg.RunEndPayload{}}
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
	err = adapter.Close(retry)
	if err != nil {
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

func TestThreadBridgeDrainsAfterLeaseCancellation(t *testing.T) {
	leaseCtx, cancelLease := context.WithCancel(context.Background())
	cancelLease()
	bus := make(chan runpkg.Event)
	core := newTestThread("thread", &runpkg.Config{}, bus, ThreadOptions{})
	adapter := core
	// Start the bridge directly to isolate its lifecycle from history reload.
	output := adapter.outputBridge.start(leaseCtx, adapter)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer adapter.Close(ctx)
	select {
	case bus <- runpkg.Event{ThreadID: "thread", RunID: "run", Type: runpkg.EventRunEnd, Payload: runpkg.RunEndPayload{}}:
	case <-ctx.Done():
		t.Fatal("canceled lease stopped the bridge before Core finished")
	}
	err := adapter.Close(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for range output.Items {
		n++
	}
	if n != 1 {
		t.Fatalf("final events=%d", n)
	}
}

func TestThreadOutputSurfacesConversionFailure(t *testing.T) {
	bus := make(chan runpkg.Event, 1)
	core := newTestThread("thread", &runpkg.Config{}, bus, ThreadOptions{})
	adapter := core
	output, err := adapter.Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer adapter.Close(ctx)
	bus <- runpkg.Event{RunID: "run", Type: runpkg.EventToolStart, Payload: "invalid payload"}
	select {
	case item := <-output.Items:
		if item.Err == nil {
			t.Fatal("conversion failure was silently discarded")
		}
	case <-ctx.Done():
		t.Fatal("missing conversion error")
	}
}

func TestThread_CancelDeliversFinalEventsBeforeInactive(t *testing.T) {
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
	events := make(chan runpkg.Event)
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: m}}, events, ThreadOptions{})
	accepted, err := thread.SubmitInput(runCtx, schema.UserMessage("go"))
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case e := <-events:
			if e.Type == runpkg.EventLLMRequesting {
				goto modeling
			}
		case <-ctx.Done():
			t.Fatal("model request event missing")
		}
	}
modeling:
	select {
	case <-m.started:
	case <-ctx.Done():
		t.Fatal("model did not start")
	}
	cancelRun()
	short, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	waitErr2 := accepted.RunHandle.Wait(short)
	if !errors.Is(waitErr2, context.DeadlineExceeded) {
		stop()
		t.Fatalf("Wait bypassed terminal delivery: %v", waitErr2)
	}
	stop()
	if thread.CurrentRun() == nil {
		t.Fatal("run inactive before terminal delivery")
	}
	shortClose, stopClose := context.WithTimeout(ctx, 20*time.Millisecond)
	threadCloseErr := thread.Close(shortClose)
	if !errors.Is(threadCloseErr, context.DeadlineExceeded) {
		stopClose()
		t.Fatalf("Close bypassed terminal delivery: %v", threadCloseErr)
	}
	stopClose()
	for _, expected := range []runpkg.EventType{runpkg.EventError, runpkg.EventRunEnd} {
		select {
		case e := <-events:
			if e.Type != expected || e.RunID != accepted.RunID {
				t.Fatalf("event=%+v expected=%s", e, expected)
			}
		case <-ctx.Done():
			t.Fatalf("missing terminal event %s", expected)
		}
	}
	waitErr := accepted.RunHandle.Wait(ctx)
	if !errors.Is(waitErr, context.Canceled) {
		t.Fatalf("lost cancellation cause: %v", waitErr)
	}
	if thread.CurrentRun() != nil {
		t.Fatal("run still active after delivery")
	}
	closeErr := thread.Close(ctx)
	if closeErr != nil {
		t.Fatalf("Close retry failed: %v", closeErr)
	}
	select {
	case e := <-events:
		t.Fatalf("late event: %+v", e)
	default:
	}
}

func TestThread_CancellationKeepsHistoryAndThreadUsable(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	m := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
	th := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: m}}, make(chan runpkg.Event, 100), ThreadOptions{})
	first, err := th.SubmitInput(runCtx, schema.UserMessage("original"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	cancel()
	err = first.RunHandle.Wait(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	second, err := th.SubmitInput(ctx, schema.UserMessage("again"))
	if err != nil {
		t.Fatal(err)
	}
	err = second.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID == second.RunID || len(m.inputs) != 2 || m.inputs[1][0].Content != "original" || m.inputs[1][1].Content != "again" {
		t.Fatalf("cancel lost history or ownership: %v", m.inputs)
	}
}

func TestThread_CloseCancelsActiveRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: m}}, make(chan runpkg.Event, 16), ThreadOptions{})
	accepted, err := thread.SubmitInput(ctx, schema.UserMessage("run"))
	if err != nil {
		t.Fatal(err)
	}
	<-m.started
	threadCloseErr := thread.Close(ctx)
	if threadCloseErr != nil {
		t.Fatal(threadCloseErr)
	}
	if thread.CurrentRun() != nil || accepted.RunHandle.IsActive() {
		t.Fatal("close returned with active run")
	}
	_, submitInputErr := thread.SubmitInput(ctx, schema.UserMessage("late"))
	if submitInputErr == nil {
		t.Fatal("closed thread accepted input")
	}
	closeErr := thread.Close(ctx)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestThreadAdapter_ToolFailureRemainsVisibleInPersistedPayload(t *testing.T) {
	var end types.ToolEndPayload
	err := json.Unmarshal([]byte(`{"Name":"write_file","CallID":"failed","Result":"permission denied","IsError":true}`), &end)
	if err != nil {
		t.Fatal(err)
	}
	_, payload, err := agentEventPayloadForOutput(runpkg.Event{Type: runpkg.EventToolEnd, Payload: end}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	err = json.Unmarshal(raw, &fields)
	if err != nil {
		t.Fatal(err)
	}
	if fields["is_error"] != true {
		t.Fatalf("failed mutation lost error status: %s", raw)
	}
}

func TestThreadOutput_ModelRequestPublishesOnlyThinkingPhase(t *testing.T) {
	output, err := workerEvent("session", "thread", runpkg.Event{
		ID: "event", RunID: "run", Type: runpkg.EventLLMRequesting,
		Payload: types.LLMRequestingPayload{Messages: []*schema.Message{schema.UserMessage("private request")}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if output == nil {
		t.Fatal("model activity was discarded before reaching the UI")
	}
	if output.Type != "agent_activity" || output.RunID != "run" || string(output.Payload) != `{"phase":"thinking"}` {
		t.Fatalf("unexpected activity: %+v", output)
	}
}
