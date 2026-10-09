package thread

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"eino-cli/deepagent/graph/execution"
	agentmodel "eino-cli/deepagent/model"
	runpkg "eino-cli/deepagent/run"

	"github.com/cloudwego/eino/schema"
)

func TestThreadAdapter_InputConsumedPreservesIndividualIdentityAndMedia(t *testing.T) {
	first, second := agentmodel.NewUserMessage("first"), agentmodel.NewUserMessage("describe")
	first.MessageID = "one"
	second.MessageID, second.SenderID, second.SenderType = "two", "person", "user"
	url := "https://example.test/image.png"
	second.UserInputMultiContent = []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeText, Text: "describe"}, {Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url, MIMEType: "image/png"}}}}
	kind, payload, err := agentEventPayloadForOutput(agentmodel.RunEvent{Type: agentmodel.EventInputConsumed, Payload: agentmodel.RunInput{Message: second}, ConsumedInputs: []*agentmodel.Message{first, second}}, nil)
	if err != nil || kind != agentmodel.EventTypeInputConsumed {
		t.Fatalf("kind=%s err=%v", kind, err)
	}
	message := payload.(*agentmodel.MessageEventPayload)
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

func TestThreadAdapter_ContextUsagePreservesTokensAndRatio(t *testing.T) {
	contextTokenUsage := agentmodel.ContextTokenUsage{
		MaxContextTokens: 10000,
		TotalTokens:      5500,
		PromptTokens:     4000,
		CompletionTokens: 500,
	}
	payload := convertContextUsagePayload(&contextTokenUsage)
	if payload == nil || payload.UsedTokens != 5500 || payload.MaxTokens == nil || *payload.MaxTokens != 10000 {
		t.Fatalf("context capacity lost: %+v", payload)
	}
	if payload.Ratio == nil || *payload.Ratio != 0.55 {
		t.Fatalf("incorrect context ratio: %+v", payload)
	}
	if payload.PromptTokens == nil || *payload.PromptTokens != 4000 || payload.CompletionTokens == nil || *payload.CompletionTokens != 500 {
		t.Fatalf("model token usage lost: %+v", payload)
	}
}

func TestThreadAdapter_FollowUpIncludesQuestion(t *testing.T) {
	payload := followUpRequiredPayload(agentmodel.FollowUpRequestedPayload{
		InterruptID: "interrupt-1", CheckpointID: "checkpoint-1",
		Info: &agentmodel.FollowUpInfo{Question: "选择哪个目录？", Questions: []string{"src", "docs"}},
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
	payload := convertApprovalRequiredPayload(agentmodel.ApprovalRequiredPayload{InterruptID: "interrupt", CheckpointID: "checkpoint", ApprovalInfo: &agentmodel.ApprovalInfo{CallID: "call", ToolName: "execute", Arguments: `{"command":"pwd"}`}})
	if payload.ToolCallID != "call" || payload.InterruptID != "interrupt" || payload.CheckpointID != "checkpoint" || payload.ToolName != "execute" || payload.ArgumentsJSON == nil {
		t.Fatalf("approval identity lost: %+v", payload)
	}
}

func TestThreadAdapter_RunEndPreservesOutcome(t *testing.T) {
	for _, status := range []string{"finished", "blocked", "interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			end := agentmodel.RunEndPayload{Status: status}
			if status == "blocked" {
				end.CheckpointID = "checkpoint"
				end.InterruptID = "interrupt"
			}
			kind, value, err := agentEventPayloadForOutput(agentmodel.RunEvent{
				Type: agentmodel.EventRunEnd, RunID: "run", Payload: end,
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if kind != agentmodel.EventTypeRunStatus {
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
	batch := agentmodel.InterruptBatchPayload{CheckpointID: "checkpoint", Items: []agentmodel.InterruptBatchItem{
		{InterruptID: "first", Kind: agentmodel.InterruptItemApprove, ApprovalInfo: &agentmodel.ApprovalInfo{CallID: "call-a", ToolName: "write_file"}},
		{InterruptID: "second", Kind: agentmodel.InterruptItemApprove, ApprovalInfo: &agentmodel.ApprovalInfo{CallID: "call-b", ToolName: "execute"}},
	}}
	kind, payload, err := agentEventPayloadForOutput(agentmodel.RunEvent{Type: agentmodel.EventInterruptBatchRequested, Payload: batch}, nil)
	if err != nil || kind != agentmodel.EventTypeInputRequired {
		t.Fatalf("kind=%s payload=%v err=%v", kind, payload, err)
	}
	out, ok := payload.(*agentmodel.InterruptBatchRequiredEventPayload)
	if !ok || len(out.Items) != 2 || out.Items[0].InterruptID != "first" || out.Items[1].InterruptID != "second" {
		t.Fatalf("payload=%+v", payload)
	}
	resume := agentmodel.ResumeRunPayload{InterruptID: "first", Answers: []agentmodel.ResumeAnswer{
		{InterruptID: "first", Approval: &agentmodel.ApprovalDecision{Approved: true}},
		{InterruptID: "second", Approval: &agentmodel.ApprovalDecision{Approved: false}},
	}}
	answers, err := resumeData(context.Background(), resume, nil)
	if err != nil || len(answers) != 2 {
		t.Fatalf("answers=%v err=%v", answers, err)
	}
	if answers["first"].(*agentmodel.ApprovalResult).Approved != true || answers["second"].(*agentmodel.ApprovalResult).Approved != false {
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
	events := make(chan agentmodel.RunEvent, 32)
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: m, CheckpointStore: &legacyParityMemoryCheckpoints{}}}, events, ThreadOptions{})
	accepted, err := thread.SubmitInput(ctx, agentmodel.NewUserMessage("wait"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	timeout := 10 * time.Millisecond
	thread.InterruptRun(agentmodel.InterruptOptions{Timeout: &timeout, Metadata: map[string]string{"reason": "handoff"}})
	thread.InterruptRun(agentmodel.InterruptOptions{Timeout: &timeout, Metadata: map[string]string{"reason": "handoff"}})
	waitErr := accepted.RunHandle.Wait(ctx)
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	found := false
	for len(events) > 0 {
		event := <-events
		if event.Type == agentmodel.EventError {
			t.Fatalf("external timeout became error: %+v", event)
		}
		if event.Type == agentmodel.EventInterrupted {
			p := event.Payload.(agentmodel.InterruptedPayload)
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
	bus := make(chan agentmodel.RunEvent, 1)
	core := newTestThread("thread", &runpkg.Config{}, bus, ThreadOptions{})
	adapter := core
	output, err := adapter.Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	bus <- agentmodel.RunEvent{ThreadID: "thread", RunID: "run", Type: agentmodel.EventRunEnd, Payload: agentmodel.RunEndPayload{}}
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
	bus := make(chan agentmodel.RunEvent, count)
	core := newTestThread("thread", &runpkg.Config{}, bus, ThreadOptions{})
	adapter := core
	output, err := adapter.Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		bus <- agentmodel.RunEvent{ThreadID: "thread", RunID: "run", Type: agentmodel.EventRunEnd, Payload: agentmodel.RunEndPayload{}}
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
	bus := make(chan agentmodel.RunEvent)
	core := newTestThread("thread", &runpkg.Config{}, bus, ThreadOptions{})
	adapter := core
	// Start the bridge directly to isolate its lifecycle from history reload.
	output := adapter.outputBridge.start(leaseCtx, adapter)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer adapter.Close(ctx)
	select {
	case bus <- agentmodel.RunEvent{ThreadID: "thread", RunID: "run", Type: agentmodel.EventRunEnd, Payload: agentmodel.RunEndPayload{}}:
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
	bus := make(chan agentmodel.RunEvent, 1)
	core := newTestThread("thread", &runpkg.Config{}, bus, ThreadOptions{})
	adapter := core
	output, err := adapter.Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	defer adapter.Close(ctx)
	bus <- agentmodel.RunEvent{RunID: "run", Type: agentmodel.EventToolStart, Payload: "invalid payload"}
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
	events := make(chan agentmodel.RunEvent)
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: m}}, events, ThreadOptions{})
	accepted, err := thread.SubmitInput(runCtx, agentmodel.NewUserMessage("go"))
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case e := <-events:
			if e.Type == agentmodel.EventLLMRequesting {
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
	for _, expected := range []agentmodel.RunEventType{agentmodel.EventError, agentmodel.EventRunEnd} {
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
	th := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: m}}, make(chan agentmodel.RunEvent, 100), ThreadOptions{})
	first, err := th.SubmitInput(runCtx, agentmodel.NewUserMessage("original"))
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
	second, err := th.SubmitInput(ctx, agentmodel.NewUserMessage("again"))
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
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: m}}, make(chan agentmodel.RunEvent, 16), ThreadOptions{})
	accepted, err := thread.SubmitInput(ctx, agentmodel.NewUserMessage("run"))
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
	_, submitInputErr := thread.SubmitInput(ctx, agentmodel.NewUserMessage("late"))
	if submitInputErr == nil {
		t.Fatal("closed thread accepted input")
	}
	closeErr := thread.Close(ctx)
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func TestThreadAdapter_ToolFailureRemainsVisibleInPersistedPayload(t *testing.T) {
	var end agentmodel.ToolEndPayload
	err := json.Unmarshal([]byte(`{"Name":"write_file","CallID":"failed","Result":"permission denied","IsError":true}`), &end)
	if err != nil {
		t.Fatal(err)
	}
	_, payload, err := agentEventPayloadForOutput(agentmodel.RunEvent{Type: agentmodel.EventToolEnd, Payload: end}, nil)
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
	output, err := workerEvent("session", "thread", agentmodel.RunEvent{
		ID: "event", RunID: "run", Type: agentmodel.EventLLMRequesting,
		Payload: agentmodel.LLMRequestingPayload{Messages: []*agentmodel.Message{agentmodel.NewUserMessage("private request")}},
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

func TestThreadAdapter_ToolScreenshotPreserved(t *testing.T) {
	image := "cG5n"
	_, payload, err := agentEventPayloadForOutput(agentmodel.RunEvent{Type: agentmodel.EventToolEnd, Payload: agentmodel.ToolEndPayload{CallID: "screen", Name: "browser_observe", MultiContent: []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &image, MIMEType: "image/png"}}}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tool := payload.(*agentmodel.ToolCallEventPayload)
	if len(tool.Parts) != 1 || tool.Parts[0].Base64Data != image {
		t.Fatalf("screenshot missing: %+v", tool)
	}
}

func TestThreadAdapter_CompactionEventsPreserveCapturedUsage(t *testing.T) {
	ctxUsage := agentmodel.ContextTokenUsage{MaxContextTokens: 10000, TotalTokens: 5500, PromptTokens: 4000, CompletionTokens: 500}
	liveUsage := agentmodel.ContextTokenUsage{MaxContextTokens: 20000, TotalTokens: 300, PromptTokens: 250, CompletionTokens: 50}
	input := agentmodel.NewUserMessage("input")
	input.MessageID = "input-1"
	for _, testCase := range []struct {
		name   string
		typeID agentmodel.RunEventType
		usage  agentmodel.ContextTokenUsage
		want   agentmodel.ContextTokenUsage
		status string
	}{
		{"started", agentmodel.EventContextCompactStarted, ctxUsage, ctxUsage, agentmodel.RunStatusCompactStarted},
		{"finished", agentmodel.EventContextCompacted, ctxUsage, ctxUsage, agentmodel.RunStatusContextCompacted},
		{"empty_started", agentmodel.EventContextCompactStarted, agentmodel.ContextTokenUsage{}, liveUsage, agentmodel.RunStatusCompactStarted},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			kind, value, err := agentEventPayloadForOutput(agentmodel.RunEvent{
				Type: testCase.typeID, Payload: testCase.usage, ConsumedInputs: []*agentmodel.Message{input},
			}, &liveUsage)
			if err != nil || kind != agentmodel.EventTypeRunStatus {
				t.Fatalf("kind=%s err=%v", kind, err)
			}
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var payload agentmodel.ContextCompactedEventPayload
			err = json.Unmarshal(raw, &payload)
			if err != nil {
				t.Fatal(err)
			}
			usage := payload.ContextUsage
			if payload.Status != testCase.status || !reflect.DeepEqual(payload.ConsumedMessageIDs, []string{"input-1"}) {
				t.Fatalf("compaction event identity lost: %s", raw)
			}
			if usage == nil || usage.UsedTokens != testCase.want.TotalTokens || usage.MaxTokens == nil || *usage.MaxTokens != testCase.want.MaxContextTokens {
				t.Fatalf("compaction used live capacity instead of captured capacity: %s", raw)
			}
			if usage.PromptTokens == nil || *usage.PromptTokens != testCase.want.PromptTokens || usage.CompletionTokens == nil || *usage.CompletionTokens != testCase.want.CompletionTokens {
				t.Fatalf("compaction lost model token usage: %s", raw)
			}
		})
	}
}
