package thread

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	dalmodel "eino-cli/deepagent/dal/model"
	"eino-cli/deepagent/graph/execution"
	"eino-cli/deepagent/graph/middleware"
	deeptools "eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	inputpkg "eino-cli/deepagent/protocol/input"
	runpkg "eino-cli/deepagent/run"

	"github.com/cloudwego/eino/schema"
)

func TestThread_UsageResetsForEachRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m := &usageThreadModel{}
	events := make(chan runpkg.Event, 100)
	th := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: m}}, events, ThreadOptions{})
	defer th.Close(context.Background())
	err := th.InitHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := th.SubmitInput(ctx, schema.UserMessage("go"))
		if err != nil {
			t.Fatal(err)
		}
		err = result.RunHandle.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(m.inputs) != 2 {
		t.Fatal("expected one model call per Run")
	}
	count := 0
	for len(events) > 0 {
		e := <-events
		if e.Type != runpkg.EventTokens {
			continue
		}
		count++
		if e.Payload.(types.Usage).TotalTokens != 5 {
			t.Fatalf("counter leaked across runs: %+v", e.Payload)
		}
	}
	if count != 2 {
		t.Fatalf("token events=%d", count)
	}
}

func TestPendingInputStaysInOneRun(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{})
	chatModel := &legacyParityScriptedModel{stream: func(ctx context.Context, index int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		if index > 0 {
			return legacyParityMessageStream(schema.AssistantMessage("second", nil))
		}
		reader, writer := schema.Pipe[*schema.Message](0)
		go func() {
			defer writer.Close()
			close(started)
			select {
			case <-gate:
				writer.Send(schema.AssistantMessage("first", nil), nil)
			case <-ctx.Done():
			}
		}()
		return reader
	}}

	events := make(chan runpkg.Event, 64)
	thread := newTestThread("thread-1", &runpkg.Config{Graph: execution.Config{
		Model: chatModel, CheckpointStore: &legacyParityMemoryCheckpoints{},
	}}, events, ThreadOptions{})
	first, err := thread.SubmitInput(context.Background(), schema.UserMessage("first"))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	second, err := thread.SubmitInput(context.Background(), schema.UserMessage("second"))
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID != second.RunID || second.Started {
		t.Fatalf("pending input started another run: first=%+v second=%+v", first, second)
	}
	close(gate)
	legacyParityWaitRunEnd(t, events)

	chatModel.mu.Lock()
	defer chatModel.mu.Unlock()
	if len(chatModel.inputs) != 2 {
		t.Fatalf("model calls = %d, want 2", len(chatModel.inputs))
	}
	last := chatModel.inputs[1]
	if len(last) == 0 || last[len(last)-1].Content != "second" {
		t.Fatalf("pending input was not delivered to the same run: %+v", last)
	}
}

func TestInterruptedRunLeavesThreadReusable(t *testing.T) {
	started := make(chan struct{})
	chatModel := &legacyParityScriptedModel{stream: func(ctx context.Context, index int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		if index > 0 {
			return legacyParityMessageStream(schema.AssistantMessage("recovered", nil))
		}
		reader, writer := schema.Pipe[*schema.Message](0)
		go func() {
			defer writer.Close()
			close(started)
			<-ctx.Done()
		}()
		return reader
	}}

	events := make(chan runpkg.Event, 64)
	thread := newTestThread("thread-1", &runpkg.Config{Graph: execution.Config{
		Model: chatModel, CheckpointStore: &legacyParityMemoryCheckpoints{},
	}}, events, ThreadOptions{})
	_, err := thread.SubmitInput(context.Background(), schema.UserMessage("original"))
	if err != nil {
		t.Fatal(err)
	}
	<-started
	handle := thread.CurrentRun()
	timeout := 200 * time.Millisecond
	if !thread.InterruptRun(runpkg.InterruptOptions{Timeout: &timeout}) {
		t.Fatal("active run did not accept interrupt")
	}

	waitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = handle.Wait(waitCtx) // An interrupted run may return its interruption error.
	if waitCtx.Err() != nil || thread.CurrentRun() != nil {
		t.Fatal("interrupted run did not finish")
	}
	// Terminal events precede completion. Drain the interrupted run before
	// asserting the next run succeeds; no extra event signals becoming inactive.
	for len(events) > 0 {
		<-events
	}
	_, submitInputErr := thread.SubmitInput(context.Background(), schema.UserMessage("again"))
	if submitInputErr != nil {
		t.Fatal(submitInputErr)
	}
	legacyParityWaitRunEnd(t, events)

	chatModel.mu.Lock()
	defer chatModel.mu.Unlock()
	if len(chatModel.inputs) != 2 {
		t.Fatalf("model calls = %d, want 2", len(chatModel.inputs))
	}
	second := chatModel.inputs[1]
	if len(second) < 2 || second[0].Content != "original" || second[len(second)-1].Content != "again" {
		t.Fatalf("history was not reusable after interrupt: %+v", second)
	}
}

func TestBlockedRunResumesFromCheckpointOnNewThread(t *testing.T) {
	chatModel := &legacyParityScriptedModel{stream: func(_ context.Context, index int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		if index == 0 {
			return legacyParityMessageStream(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
				ID: "question-1", Type: "function", Function: schema.FunctionCall{
					Name: "ask_user", Arguments: `{"question":"Which format?","options":["JSON","YAML"]}`,
				},
			}}})
		}
		return legacyParityMessageStream(schema.AssistantMessage("using YAML", nil))
	}}
	checkpoints := &legacyParityMemoryCheckpoints{}
	config := &runpkg.Config{Graph: execution.Config{
		Model: chatModel, CheckpointStore: checkpoints,
		ToolDescriptors: []deeptools.ToolDescriptor{deeptools.NewFollowUpTool()},
	}}

	firstEvents := make(chan runpkg.Event, 64)
	first := newTestThread("thread-1", config, firstEvents, ThreadOptions{})
	started, err := first.SubmitInput(context.Background(), schema.UserMessage("export"))
	if err != nil {
		t.Fatal(err)
	}
	var blocked runpkg.FollowUpRequestedPayload
	deadline := time.After(5 * time.Second)
waitBlocked:
	for {
		select {
		case event := <-firstEvents:
			switch event.Type {
			case runpkg.EventFollowUpRequested:
				blocked = event.Payload.(runpkg.FollowUpRequestedPayload)
				break waitBlocked
			case runpkg.EventError:
				t.Fatalf("first run failed: %+v", event.Payload)
			}
		case <-deadline:
			t.Fatal("timed out waiting for follow-up request")
		}
	}
	for first.CurrentRun() != nil {
		time.Sleep(time.Millisecond)
	}
	if blocked.CheckpointID == "" || blocked.InterruptID == "" {
		t.Fatalf("incomplete block: %+v", blocked)
	}

	secondEvents := make(chan runpkg.Event, 64)
	second := newTestThread("thread-1", config, secondEvents, ThreadOptions{})
	_, err = second.ResumeRun(context.Background(), started.RunID, ResumeRunOptions{
		CheckpointID: blocked.CheckpointID,
		ResumeData: map[string]any{
			blocked.InterruptID: &deeptools.FollowUpInfo{UserAnswer: "YAML"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	legacyParityWaitRunEnd(t, secondEvents)

	chatModel.mu.Lock()
	defer chatModel.mu.Unlock()
	if len(chatModel.inputs) != 2 {
		t.Fatalf("model calls = %d, want 2", len(chatModel.inputs))
	}
	messages := chatModel.inputs[1]
	if len(messages) == 0 || messages[len(messages)-1].Role != schema.Tool || messages[len(messages)-1].Content != "YAML" {
		t.Fatalf("resume answer did not reach checkpointed tool call: %+v", messages)
	}
}

func TestReloadRepairsInterruptedToolCallBeforeNewInput(t *testing.T) {
	store := &legacyParityDedupConversationRepository{
		seen: map[int64]struct{}{1: {}, 2: {}},
		records: []*dalmodel.ConversationEntry{
			{Type: dalmodel.ConversationEntryMessage, ThreadID: "thread-1", MessageID: 1, Seq: 1, Message: schema.UserMessage("previous")},
			{Type: dalmodel.ConversationEntryMessage, ThreadID: "thread-1", MessageID: 2, Seq: 2, Message: &schema.Message{
				Role:      schema.Assistant,
				ToolCalls: []schema.ToolCall{{ID: "call-1", Type: "function", Function: schema.FunctionCall{Name: "write_file", Arguments: `{}`}}},
			}},
		},
	}
	chatModel := &legacyParityScriptedModel{stream: func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message] {
		return legacyParityMessageStream(schema.AssistantMessage("recovered", nil))
	}}
	events := make(chan runpkg.Event, 64)
	thread := newTestThread("thread-1", &runpkg.Config{Graph: execution.Config{
		Model: chatModel, CheckpointStore: &legacyParityMemoryCheckpoints{},
	}}, events, ThreadOptions{ConversationRepository: store})
	err := thread.InitHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, submitInputErr := thread.SubmitInput(context.Background(), schema.UserMessage("new task"))
	if submitInputErr != nil {
		t.Fatal(submitInputErr)
	}
	legacyParityWaitRunEnd(t, events)

	chatModel.mu.Lock()
	defer chatModel.mu.Unlock()
	if len(chatModel.inputs) != 1 {
		t.Fatalf("model calls = %d, want 1", len(chatModel.inputs))
	}
	messages := chatModel.inputs[0]
	if len(messages) != 4 || messages[2].Role != schema.Tool || messages[2].ToolCallID != "call-1" || messages[3].Content != "new task" {
		t.Fatalf("interrupted tool history was not repaired: %+v", messages)
	}
	for _, message := range thread.ContextManager().GetHistory(context.Background()) {
		if message.Role == schema.Tool && message.ToolCallID == "call-1" {
			t.Fatal("synthetic repair was persisted as a real tool result")
		}
	}
}

func TestRunBudgetsStopUnboundedToolLoop(t *testing.T) {
	for _, test := range []struct {
		name          string
		maxSteps      int
		maxModelCalls int
	}{
		{name: "model calls", maxSteps: 20, maxModelCalls: 1},
		{name: "graph steps", maxSteps: 1, maxModelCalls: 20},
	} {
		t.Run(test.name, func(t *testing.T) {
			chatModel := &legacyParityScriptedModel{stream: func(context.Context, int, []*schema.Message) *schema.StreamReader[*schema.Message] {
				return legacyParityMessageStream(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
					ID: "call", Type: "function", Function: schema.FunctionCall{Name: "echo", Arguments: `{}`},
				}}})
			}}
			events := make(chan runpkg.Event, 64)
			thread := newTestThread("thread-1", &runpkg.Config{Graph: execution.Config{
				Model: chatModel, ToolDescriptors: []deeptools.ToolDescriptor{{Tool: legacyParityEchoTool{}}},
				MaxSteps: test.maxSteps, MaxModelCalls: test.maxModelCalls,
				CheckpointStore: &legacyParityMemoryCheckpoints{},
			}}, events, ThreadOptions{})
			_, err := thread.SubmitInput(context.Background(), schema.UserMessage("loop"))
			if err != nil {
				t.Fatal(err)
			}
			deadline := time.After(5 * time.Second)
			for {
				select {
				case event := <-events:
					if event.Type == runpkg.EventError {
						return
					}
				case <-deadline:
					t.Fatal("budget did not stop tool loop")
				}
			}
		})
	}
}

func TestManagerMessageIDSurvivesInputDecoding(t *testing.T) {
	command, err := decodeUserInputCommand(&TransportMessage{
		ID:      "2000000000000000042",
		Type:    MessageTypeInput,
		Payload: []byte(`{"parts":[{"type":"text","text":"hello"}]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	got := MessageID(command.schema)
	if got != "2000000000000000042" {
		t.Fatalf("message id = %q", got)
	}
	if command.input.Parts[0].Type != inputpkg.MessagePartTypeText {
		t.Fatalf("unexpected input: %+v", command.input)
	}
}

func TestInputDecodingPreservesMultimediaParts(t *testing.T) {
	command, err := decodeUserInputCommand(&TransportMessage{
		ID:   "43",
		Type: MessageTypeInput,
		Payload: []byte(`{"parts":[
			{"type":"text","text":"describe"},
			{"type":"image","url":"https://example.test/photo.png","mime_type":"image/png"}
		]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	parts := command.schema.UserInputMultiContent
	if len(parts) != 2 || parts[1].Image == nil || parts[1].Image.URL == nil || *parts[1].Image.URL != "https://example.test/photo.png" {
		t.Fatalf("multimedia input was flattened: %+v", command.schema)
	}
}

func TestThread_MultimodalRoundTrip(t *testing.T) {
	command, err := decodeUserInputCommand(&TransportMessage{
		ID:   "9007199254740993",
		Type: MessageTypeInput,
		Payload: []byte(`{"parts":[
			{"type":"text","text":"describe","extra":{"language":"zh"}},
			{"type":"image","url":"https://example.test/photo.png","mime_type":"image/png","detail":"high"},
			{"type":"audio","base64_data":"YXVkaW8=","mime_type":"audio/wav"},
			{"type":"video","url":"https://example.test/clip.mp4","mime_type":"video/mp4"},
			{"type":"file","url":"https://example.test/report.pdf","name":"report.pdf","mime_type":"application/pdf"}
		]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	copy := types.CopyMessage(command.schema)
	got := MessageID(copy)
	if got != "9007199254740993" {
		t.Fatalf("message ID changed: %q", got)
	}
	want := inputPartsForEvent(command.input.Parts)
	schemaUserMessageToProtocolPartsGot := schemaUserMessageToProtocolParts(copy)
	if !reflect.DeepEqual(schemaUserMessageToProtocolPartsGot, want) {
		t.Fatalf("multimodal event parts changed: got %+v, want %+v", schemaUserMessageToProtocolPartsGot, want)
	}
}

func TestThread_RedeliveryPreservesMessageIdentity(t *testing.T) {
	ctx := context.Background()
	store := &redeliveryConversationRepository{}
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: &redeliveryModel{}}}, make(chan runpkg.Event, 128), ThreadOptions{ConversationRepository: store, ConversationEntryID: func(_ context.Context, _, _ string, message *schema.Message) (int64, error) {
		id, err := strconv.ParseInt(MessageID(message), 10, 64)
		if err != nil {
			return 0, nil // The test store assigns assistant IDs.
		}
		return id, nil
	}})
	initHistoryErr := thread.InitHistory(ctx)
	if initHistoryErr != nil {
		t.Fatal(initHistoryErr)
	}
	for range 2 {
		command, err := decodeUserInputCommand(&TransportMessage{ID: "9007199254740993", Type: MessageTypeInput, Payload: []byte(`{"parts":[{"type":"text","text":"hello"}]}`)})
		if err != nil {
			t.Fatal(err)
		}
		accepted, err := thread.SubmitInput(ctx, types.CopyMessage(command.schema))
		if err != nil {
			t.Fatal(err)
		}
		waitErr := accepted.RunHandle.Wait(ctx)
		if waitErr != nil {
			t.Fatal(waitErr)
		}
	}
	userCount := 0
	for _, record := range store.records {
		if record.Message != nil && record.Message.Role == schema.User {
			userCount++
			if record.MessageID != 9007199254740993 || MessageID(record.Message) != "9007199254740993" {
				t.Fatalf("redelivery changed message identity: %+v", record)
			}
		}
	}
	if userCount != 1 {
		t.Fatalf("redelivery wrote %d user messages", userCount)
	}
}

// A finished execution must leave the same Thread ready for another Run,
// while retaining the history consumed by the next model request.
func TestThreadOwnsSuccessiveRunsAndHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	m := &publicModel{}
	thread, err := NewThread(ThreadConfig{
		ThreadID:  "thread",
		RunConfig: &runpkg.Config{Graph: execution.Config{Model: m}},
		Events:    make(chan runpkg.Event, 128),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer thread.Close(ctx)
	err = thread.InitHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := thread.SubmitInput(ctx, schema.UserMessage("first"), WithMessageID("1"))
	if err != nil {
		t.Fatal(err)
	}
	err = first.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := thread.SubmitInput(ctx, schema.UserMessage("second"), WithMessageID("2"))
	if err != nil {
		t.Fatal(err)
	}
	err = second.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID == second.RunID || !second.Started || thread.CurrentRun() != nil {
		t.Fatal("completed execution did not release the Thread for a distinct Run")
	}
	if len(m.inputs) != 2 || len(m.inputs[1]) != 3 || m.inputs[1][0].Content != "first" || m.inputs[1][2].Content != "second" {
		t.Fatalf("next Run lost Thread history: %+v", m.inputs)
	}
	repeated, err := thread.SubmitInput(ctx, schema.UserMessage("first"), WithMessageID("1"))
	if err != nil {
		t.Fatal(err)
	}
	if repeated.RunID != first.RunID || repeated.Started || len(m.inputs) != 2 {
		t.Fatal("redelivery started a new execution instead of returning original ownership")
	}
}

// Closing a Thread must not close its filesystem while model work is still
// outstanding, even when the first Close caller times out.
func TestThreadCloseWaitsBeforeClosingResources(t *testing.T) {
	ctx := context.Background()
	m := &threadCloseModel{started: make(chan struct{}), release: make(chan struct{})}
	closed := 0
	thread, err := NewThread(ThreadConfig{
		ThreadID:       "thread",
		RunConfig:      &runpkg.Config{Graph: execution.Config{Model: m}},
		CloseResources: func(context.Context) error { closed++; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	accepted, err := thread.SubmitInput(ctx, schema.UserMessage("wait"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-m.started:
	case <-time.After(time.Second):
		t.Fatal("model did not start")
	}
	closeCtx, cancel := context.WithTimeout(ctx, 10*time.Millisecond)
	defer cancel()
	err = thread.Close(closeCtx)
	if !errors.Is(err, context.DeadlineExceeded) || closed != 0 {
		t.Fatalf("closed resources before execution settled: error=%v closes=%d", err, closed)
	}
	close(m.release)
	waitCtx, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	_ = accepted.RunHandle.Wait(waitCtx)
	err = thread.Close(waitCtx)
	if err != nil || closed != 1 {
		t.Fatalf("cleanup error=%v closes=%d", err, closed)
	}
	err = thread.Close(waitCtx)
	if err != nil || closed != 1 {
		t.Fatalf("duplicate cleanup error=%v closes=%d", err, closed)
	}
}

func TestThreadCancellationBeforeGraphClosesMiddleware(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	mw := &threadCloseMiddleware{ready: make(chan struct{})}
	events := make(chan runpkg.Event)
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: &publicModel{}, Middlewares: []middleware.Middleware{mw}}}, events, ThreadOptions{})
	accepted, err := thread.SubmitInput(runCtx, schema.UserMessage("go"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-mw.ready:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// RunStart cannot be delivered on this unbuffered channel before cancellation.
	cancel()
draining:
	for {
		select {
		case event := <-events:
			if event.Type == runpkg.EventRunEnd {
				break draining
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	err = accepted.RunHandle.Wait(ctx)
	if !errors.Is(err, context.Canceled) || mw.closed.Load() != 1 {
		t.Fatalf("early cancellation leaked middleware: error=%v closes=%d", err, mw.closed.Load())
	}
}

func TestThread_SubmitAndAppendUseSameRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	model := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
	events := make(chan runpkg.Event, 100)
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: model}}, events, ThreadOptions{})
	initHistoryErr := thread.InitHistory(ctx)
	if initHistoryErr != nil {
		t.Fatal(initHistoryErr)
	}
	first, err := thread.SubmitInput(ctx, schema.UserMessage("first"), WithInputMeta("id-1"))
	if err != nil {
		t.Fatal(err)
	}
	<-model.started
	second, err := thread.SubmitInput(ctx, schema.UserMessage("second"), WithInputMeta("id-2"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Started || second.RunID != first.RunID {
		t.Fatal("append assigned to different run")
	}
	close(model.release)
	waitErr := first.RunHandle.Wait(ctx)
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	if model.calls != 2 {
		t.Fatalf("calls=%d", model.calls)
	}
	inputs := first.RunHandle.ConsumedInputs()
	meta := first.RunHandle.ConsumedInputsMeta()
	if len(inputs) != 2 || inputs[1].Content != "second" || len(meta) != 2 || meta[1] != "id-2" {
		t.Fatalf("lost input identity: %v %v", inputs, meta)
	}
}

func TestThread_SubmitInputRejectsInvalidMessageWhileIdle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	history := &historyMemory{}
	model := &threadModel{}
	events := make(chan runpkg.Event, 100)
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: model}}, events, ThreadOptions{ConversationRepository: history})

	unsupported := schema.UserMessage("unsupported")
	unsupported.Extra = map[string]any{"value": func() {}}
	cyclic := schema.UserMessage("cyclic")
	cyclicExtra := map[string]any{}
	cyclicExtra["self"] = cyclicExtra
	cyclic.Extra = cyclicExtra
	inputs := []struct {
		name    string
		message *schema.Message
	}{
		{name: "unsupported extra", message: unsupported},
		{name: "cyclic extra", message: cyclic},
	}

	for _, input := range inputs {
		t.Run(input.name, func(t *testing.T) {
			result, err := thread.SubmitInput(ctx, input.message)
			if err == nil {
				t.Fatal("accepted message that could not be copied")
			}
			if result != nil {
				t.Fatal("returned a run for a rejected message")
			}
			if thread.CurrentRun() != nil {
				t.Fatal("started a run for a rejected message")
			}
			if model.calls != 0 {
				t.Fatalf("model calls=%d", model.calls)
			}
			if len(history.records) != 0 {
				t.Fatalf("history records=%d", len(history.records))
			}
			if len(events) != 0 {
				t.Fatalf("events=%d", len(events))
			}
		})
	}
}

func TestThread_SubmitInputRejectsInvalidMessageWhileActive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	model := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: model}}, make(chan runpkg.Event, 100), ThreadOptions{})
	first, err := thread.SubmitInput(ctx, schema.UserMessage("first"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-model.started:
	case <-ctx.Done():
		t.Fatalf("model did not start: %v", ctx.Err())
	}

	unsupported := schema.UserMessage("unsupported")
	unsupported.Extra = map[string]any{"value": func() {}}
	cyclic := schema.UserMessage("cyclic")
	cyclicExtra := map[string]any{}
	cyclicExtra["self"] = cyclicExtra
	cyclic.Extra = cyclicExtra
	inputs := []struct {
		name    string
		message *schema.Message
	}{
		{name: "unsupported extra", message: unsupported},
		{name: "cyclic extra", message: cyclic},
	}
	for _, input := range inputs {
		t.Run(input.name, func(t *testing.T) {
			result, err := thread.SubmitInput(ctx, input.message)
			if err == nil {
				t.Fatal("accepted message that could not be copied")
			}
			if result != nil {
				t.Fatal("returned a run for a rejected message")
			}
			thread.mu.Lock()
			pending := len(thread.pending)
			thread.mu.Unlock()
			if pending != 0 {
				t.Fatalf("pending inputs=%d", pending)
			}
		})
	}

	close(model.release)
	err = first.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if model.calls != 1 {
		t.Fatalf("model calls=%d", model.calls)
	}
	consumedInputs := first.RunHandle.ConsumedInputs()
	if len(consumedInputs) != 1 || consumedInputs[0].Content != "first" {
		t.Fatalf("consumed inputs=%v", consumedInputs)
	}
}

func TestRun_NoEventsAfterActiveRunBecomesNil(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	events := make(chan runpkg.Event) // Force producer to wait for each event publication.
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: &threadModel{}}}, events, ThreadOptions{})
	result, err := thread.SubmitInput(ctx, schema.UserMessage("input"))
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case event := <-events:
			if event.Type == runpkg.EventRunEnd {
				err := result.RunHandle.Wait(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if thread.CurrentRun() != nil {
					t.Fatal("still active after Wait")
				}
				select {
				case e := <-events:
					t.Fatalf("late event: %v", e.Type)
				default:
				}
				return
			}
			if thread.CurrentRun() == nil {
				t.Fatalf("inactive before event %s", event.Type)
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func TestThread_InputAcceptedAtFinishBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: &threadModel{}}}, make(chan runpkg.Event, 10000), ThreadOptions{})
	for i := 0; i < 50; i++ {
		first, err := thread.SubmitInput(ctx, schema.UserMessage("first"))
		if err != nil {
			t.Fatal(err)
		}
		second, err := thread.SubmitInput(ctx, schema.UserMessage("boundary"))
		if err != nil {
			t.Fatal(err)
		}
		waitErr2 := first.RunHandle.Wait(ctx)
		if waitErr2 != nil {
			t.Fatal(waitErr2)
		}
		waitErr := second.RunHandle.Wait(ctx)
		if waitErr != nil {
			t.Fatal(waitErr)
		}
		found := false
		for _, input := range second.RunHandle.ConsumedInputs() {
			if input.Content == "boundary" {
				found = true
			}
		}
		if !found {
			t.Fatal("accepted input orphaned at finish boundary")
		}
	}
}

func TestRun_InterruptAndResumeOnNewThread(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	history := &historyMemory{}
	checkpoints := &threadCheckpointMemory{}
	m := &resumeModel{}
	config := &runpkg.Config{Graph: execution.Config{Model: m, CheckpointStore: checkpoints, ToolDescriptors: []deeptools.ToolDescriptor{deeptools.NewFollowUpTool()}}}
	events := make(chan runpkg.Event, 100)
	first := newTestThread("thread", config, events, ThreadOptions{ConversationRepository: history})
	firstInitHistoryErr := first.InitHistory(ctx)
	if firstInitHistoryErr != nil {
		t.Fatal(firstInitHistoryErr)
	}
	started, err := first.SubmitInput(ctx, schema.UserMessage("ask me"), WithInputMeta(map[string]string{"MessageID": "9007199254740993", "Sender": "user"}))
	if err != nil {
		t.Fatal(err)
	}
	waitErr2 := started.RunHandle.Wait(ctx)
	if waitErr2 != nil {
		t.Fatal(waitErr2)
	}
	var question runpkg.FollowUpRequestedPayload
	for len(events) > 0 {
		event := <-events
		if event.Type == runpkg.EventFollowUpRequested {
			question = event.Payload.(runpkg.FollowUpRequestedPayload)
		}
	}
	if question.InterruptID == "" || question.Info.Question != "which one?" {
		t.Fatalf("missing follow-up: %+v", question)
	}
	restored := newTestThread("thread", config, events, ThreadOptions{ConversationRepository: history})
	restoredInitHistoryErr := restored.InitHistory(ctx)
	if restoredInitHistoryErr != nil {
		t.Fatal(restoredInitHistoryErr)
	}
	hookCalled := false
	bad, badErr := restored.ResumeRun(ctx, started.RunID, ResumeRunOptions{CheckpointID: question.CheckpointID, ResumeData: map[string]any{"wrong-interrupt": &deeptools.FollowUpInfo{UserAnswer: "a"}}, OnRunStart: func(ctx context.Context, _ RunStartRequest) context.Context { hookCalled = true; return ctx }})
	if badErr == nil {
		_ = bad.Wait(ctx)
		t.Fatal("uncorrelated resume accepted")
	}
	if bad != nil || hookCalled || restored.CurrentRun() != nil || m.calls != 1 {
		t.Fatal("rejected resume performed work")
	}
	handle, err := restored.ResumeRun(ctx, started.RunID, ResumeRunOptions{CheckpointID: question.CheckpointID, ResumeData: map[string]any{question.InterruptID: &deeptools.FollowUpInfo{UserAnswer: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	waitErr := handle.Wait(ctx)
	if waitErr != nil {
		t.Fatal(waitErr)
	}
	meta := handle.ConsumedInputsMeta()
	if len(meta) != 1 {
		t.Fatalf("lost metadata: %v", meta)
	}
	identity, ok := meta[0].(map[string]string)
	if !ok || identity["MessageID"] != "9007199254740993" || identity["Sender"] != "user" {
		t.Fatalf("changed typed identity: %#v", meta)
	}
	if m.calls != 2 || len(m.inputs[1]) != 3 || m.inputs[1][2].Content != "a" {
		t.Fatalf("resume restarted model or lost tool answer: calls=%d inputs=%v", m.calls, m.inputs)
	}
	replay := newTestThread("thread", config, events, ThreadOptions{ConversationRepository: history})
	initHistoryErr := replay.InitHistory(ctx)
	if initHistoryErr != nil {
		t.Fatal(initHistoryErr)
	}
	handle, err = replay.ResumeRun(ctx, started.RunID, ResumeRunOptions{CheckpointID: question.CheckpointID, ResumeData: map[string]any{question.InterruptID: &deeptools.FollowUpInfo{UserAnswer: "a"}}})
	if err == nil {
		_ = handle.Wait(ctx)
		t.Fatal("completed checkpoint accepted as a new active run")
	}
	if handle != nil || replay.CurrentRun() != nil || m.calls != 2 {
		t.Fatal("rejected resume performed work")
	}
}

func TestThread_RedeliveryWithSameMessageIDDoesNotCallModelAgain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m := &threadModel{started: make(chan struct{}), release: make(chan struct{})}
	thread := newTestThread("thread", &runpkg.Config{Graph: execution.Config{Model: m}}, make(chan runpkg.Event, 100), ThreadOptions{})
	err := thread.InitHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := thread.SubmitInput(ctx, schema.UserMessage("first"), WithMessageID("same"))
	if err != nil {
		t.Fatal(err)
	}
	<-m.started
	_, err = thread.SubmitInput(ctx, schema.UserMessage("first"), WithMessageID("same"))
	if err != nil {
		t.Fatal(err)
	}
	close(m.release)
	err = first.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.calls != 1 || len(first.RunHandle.ConsumedInputs()) != 1 {
		t.Fatalf("redelivery repeated model work: calls=%d inputs=%v", m.calls, first.RunHandle.ConsumedInputs())
	}
	late, err := thread.SubmitInput(ctx, schema.UserMessage("first"), WithMessageID("same"))
	if err != nil {
		t.Fatal(err)
	}
	err = late.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if late.Started || late.RunID != first.RunID || m.calls != 1 {
		t.Fatalf("late redelivery started another Run: result=%+v calls=%d", late, m.calls)
	}
}

func TestRun_ResumeDeduplicatesCheckpointInputAndKeepsFollowUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	m := &resumedInputModel{started: make(chan struct{}), release: make(chan struct{})}
	checkpoints := &threadCheckpointMemory{}
	history := &historyMemory{}
	cfg := &runpkg.Config{Graph: execution.Config{Model: m, CheckpointStore: checkpoints, ToolDescriptors: []deeptools.ToolDescriptor{deeptools.NewFollowUpTool()}}}
	events := make(chan runpkg.Event, 100)
	first := newTestThread("thread", cfg, events, ThreadOptions{ConversationRepository: history})
	err := first.InitHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	started, err := first.SubmitInput(ctx, schema.UserMessage("ask me"), WithMessageID("original"))
	if err != nil {
		t.Fatal(err)
	}
	err = started.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var question runpkg.FollowUpRequestedPayload
	for len(events) > 0 {
		event := <-events
		if event.Type == runpkg.EventFollowUpRequested {
			question = event.Payload.(runpkg.FollowUpRequestedPayload)
		}
	}
	restored := newTestThread("thread", cfg, events, ThreadOptions{ConversationRepository: history})
	err = restored.InitHistory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := restored.ResumeRun(ctx, started.RunID, ResumeRunOptions{CheckpointID: question.CheckpointID, ResumeData: map[string]any{question.InterruptID: &deeptools.FollowUpInfo{UserAnswer: "a"}}})
	if err != nil {
		t.Fatal(err)
	}
	<-m.started
	_, err = restored.SubmitInput(ctx, schema.UserMessage("ask me"), WithMessageID("original"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = restored.SubmitInput(ctx, schema.UserMessage("follow-up"), WithMessageID("follow-up"))
	if err != nil {
		t.Fatal(err)
	}
	close(m.release)
	err = handle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	inputs := handle.ConsumedInputs()
	if m.calls != 3 || len(inputs) != 2 || inputs[1].Content != "follow-up" {
		t.Fatalf("calls=%d inputs=%v", m.calls, inputs)
	}
	count := 0
	for _, message := range m.inputs[2] {
		if message.Role == schema.User && message.Content == "ask me" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("restored input repeated in model history: %v", m.inputs[2])
	}
	late, err := restored.SubmitInput(ctx, schema.UserMessage("ask me"), WithMessageID("original"))
	if err != nil {
		t.Fatal(err)
	}
	err = late.RunHandle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if late.Started || late.RunID != started.RunID || m.calls != 3 {
		t.Fatalf("completed resume lost input ownership: result=%+v calls=%d", late, m.calls)
	}
}
