package deepagents

import (
	context "context"
	"sync"

	tools "eino-cli/deepagent/core/tools"
	coretypes "eino-cli/deepagent/core/types"
	eventpkg "eino-cli/deepagent/protocol/event"
	inputpkg "eino-cli/deepagent/protocol/input"
	json "encoding/json"
	errors "errors"
	fmt "fmt"
	schemapkg "github.com/cloudwego/eino/schema"
	uuid "github.com/google/uuid"
	maps "maps"
	strconv "strconv"
	strings "strings"
	time "time"
)

// Thread 输入与模型交互
// thread

// ApprovalRememberer records a session-scoped approval reuse decision.
type ApprovalRememberer interface {
	RememberApproval(ctx context.Context, payload inputpkg.ResumeRunPayload)
}

type ApprovalRemembererFunc func(ctx context.Context, payload inputpkg.ResumeRunPayload)

func (f ApprovalRemembererFunc) RememberApproval(ctx context.Context, payload inputpkg.ResumeRunPayload) {
	f(ctx, payload)
}

// RunFinishedObserver is called after a Run run-end event is converted
// to worker output. Implementations should return quickly.
type RunFinishedObserver func(ctx context.Context, ev Event)

// ThreadOutputObservation is a read-only snapshot of one worker output item
// emitted by the Thread runtime.
type ThreadOutputObservation struct {
	SessionID string
	ThreadID  string
	Item      TransportThreadOutputItem
}

// ThreadOutputObserver is called after the Thread runtime has
// successfully offered one output item to the worker host. Implementations
// should return quickly and must not rely on mutating the observed item.
type ThreadOutputObserver func(ctx context.Context, obs ThreadOutputObservation)

// InterruptResumeDecoder converts a generic Run interrupt resume payload
// into the typed data expected by a custom Eino interrupt handler.
type InterruptResumeDecoder func(ctx context.Context, payload inputpkg.ResumeRunPayload) (any, error)

// ThreadConfig builds one Thread; there is no separate protocol adapter object.
type ThreadConfig struct {
	// CloseResources runs once after execution and output forwarding stop.
	CloseResources       func(context.Context) error
	SessionID            string
	ThreadID             string
	UserID               int64
	RunConfig            *RunConfig
	Events               chan Event
	Options              ThreadOptions
	ApprovalRemember     ApprovalRememberer
	RunFinishedObserver  RunFinishedObserver
	ThreadOutputObserver ThreadOutputObserver
	InterruptResume      InterruptResumeDecoder
}

func (t *Thread) Init(ctx context.Context) (*TransportThreadOutput, error) {
	ctx = t.withThreadInfo(ctx)
	{
		err := t.ensureOpen()
		if err != nil {
			return nil, err
		}
	}
	{
		err := t.InitHistory(ctx)
		if err != nil {
			return nil, err
		}
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, TransportErrThreadClosed
	}
	if t.outputBridge.output != nil {
		return t.outputBridge.output, nil
	}

	t.startThreadOutputObserver(ctx)
	return t.outputBridge.start(ctx, t), nil
}

// PostMessage 将 Worker 消息按类型分派到输入、恢复或压缩入口。
func (t *Thread) PostMessage(ctx context.Context, message *TransportMessage) (posted *TransportPostMessageResult, err error) {
	ctx = t.withThreadInfo(ctx)
	if message == nil {
		return nil, fmt.Errorf("worker message is required")
	}
	err = t.ensureOpen()
	if err != nil {

		return nil, err
	}

	switch message.Type {
	case MessageTypeInput:
		cmd, err := decodeUserInputCommand(message)
		if err != nil {
			return nil, err
		}
		opts := []SubmitInputOption{WithMessageID(workerMessageID(cmd.message)), WithPlan(cmd.mode == inputpkg.UserMessageModeImplPlan)}
		if cmd.message != nil && len(cmd.message.Metadata) > 0 {
			opts = append(opts, WithInputMeta(maps.Clone(cmd.message.Metadata)))
		}
		opts = append(opts, WithRunStartHook(func(runCtx context.Context, req RunStartRequest) context.Context {
			return ContextContextWithRunIdentity(runCtx, ContextRunIdentity{
				ThreadID:  t.threadInfo.ThreadID,
				RunID:     req.RunID,
				MessageID: workerMessageID(cmd.message),
			})
		}))
		result, err := t.SubmitInput(ctx, cmd.schema, opts...)
		if err != nil {
			return nil, fmt.Errorf("submit input: %w", err)
		}
		if result == nil {
			return nil, fmt.Errorf("submit input returned nil result")
		}
		return &TransportPostMessageResult{RunID: result.RunID}, nil
	case MessageTypeResumeRun:
		cmd, err := decodeResumeRunCommand(message)
		if err != nil {
			return nil, err
		}
		return t.postResumeRun(ctx, cmd)
	case MessageTypeCompact:
		cmd := decodeCompactCommand(message)
		err := t.postCompact(ctx, cmd)
		if err != nil {
			return nil, err
		}
		return &TransportPostMessageResult{RunID: cmd.runID}, nil
	default:
		return nil, unsupportedRuntimeCommand(message)
	}
}

func (t *Thread) Interrupt(ctx context.Context, req TransportThreadInterruptRequest) error {
	ctx = t.withThreadInfo(ctx)

	if t.interruptCompact(ctx, req) {
		return nil
	}
	if t.InterruptRun(InterruptOptions{Timeout: req.Timeout, Metadata: threadInterruptMetadata(req)}) {
		return nil
	}
	if t.ActiveRun() == nil {
		return nil
	}
	return fmt.Errorf("interrupt active turn failed: kind=%s control_message_id=%s", req.Kind, req.ControlMessageID)
}

func (t *Thread) ActiveRun() *TransportActiveRun {
	if t == nil {
		return nil
	}
	{
		compact := t.activeCompact()
		if compact != nil {
			return &TransportActiveRun{
				RunID:              compact.runID,
				ConsumedMessageIDs: append([]string(nil), compact.consumedMessageIDs...),
			}
		}
	}
	curRun := t.CurrentRun()
	if curRun == nil {
		return nil
	}
	return &TransportActiveRun{
		RunID:              curRun.RunID(),
		ConsumedMessageIDs: ConsumedMessageIDs(curRun.ConsumedInputs()),
	}
}

func (t *Thread) Close(ctx context.Context) error {
	t.closeMu.Lock()
	defer t.closeMu.Unlock()
	t.mu.Lock()
	t.closed = true
	r := t.current
	if r != nil {
		r.accepting = false
		r.cancel(context.Canceled)
	}
	bridge := t.outputBridge
	cancelObserver := t.observerCancel
	compact := t.compact
	t.mu.Unlock()
	if compact != nil {
		t.interruptCompact(ctx, TransportThreadInterruptRequest{Kind: TransportThreadInterruptKindCloseThread})
		select {
		case <-compact.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Execution must finish before its output bridge and filesystem stop.
	if r != nil {
		select {
		case <-r.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var err error
	if cancelObserver != nil {
		cancelObserver()
	}
	if bridge != nil {
		err = bridge.stopAndWait(ctx)
		if err != nil {
			return err
		}
	}
	if t.closeResources != nil && !t.resourcesClosed {
		err = t.closeResources(ctx)
		if err != nil {
			return err
		}
		t.resourcesClosed = true
	}
	return nil
}

func (t *Thread) postResumeRun(ctx context.Context, cmd resumeRunCommand) (posted *TransportPostMessageResult, err error) {
	payload := cmd.payload
	if payload.Approval != nil && payload.Approval.CancelRun {
		t.emitCancelRunEvents(ctx, payload)
		return &TransportPostMessageResult{RunID: payload.RunID}, nil
	}

	resumeData, err := t.resumeData(ctx, payload)
	if err != nil {

		return nil, err
	}

	if payload.Approval != nil && payload.Approval.AllowInSession && payload.Approval.Approved && t.approvalRemember != nil {
		t.approvalRemember.RememberApproval(ctx, payload)
	}

	resumeIDs := []string{payload.InterruptID}
	if len(payload.Answers) > 0 {
		resumeIDs = resumeIDs[:0]
		for _, answer := range payload.Answers {
			resumeIDs = append(resumeIDs, answer.InterruptID)
		}
	}
	enablePlan := cmd.mode == inputpkg.UserMessageModeImplPlan
	opts := ResumeRunOptions{
		EnablePlan:         &enablePlan,
		CheckpointID:       payload.CheckpointID,
		ResumeInterruptIDs: resumeIDs,
		ResumeData:         resumeData,
		OnRunStart: func(runCtx context.Context, req RunStartRequest) context.Context {
			return ContextContextWithRunIdentity(runCtx, ContextRunIdentity{
				ThreadID:  t.threadInfo.ThreadID,
				RunID:     req.RunID,
				MessageID: workerMessageID(cmd.message),
			})
		},
	}
	_, err = t.ResumeRun(ctx, payload.RunID, opts)
	if err != nil {
		return nil, fmt.Errorf("resume turn: %w", err)
	}
	return &TransportPostMessageResult{RunID: payload.RunID}, nil
}

func (t *Thread) resumeData(ctx context.Context, payload inputpkg.ResumeRunPayload) (map[string]any, error) {
	return resumeData(ctx, payload, t.interruptResume)
}

func (t *Thread) emitCancelRunEvents(ctx context.Context, payload inputpkg.ResumeRunPayload) {
	reason := strings.TrimSpace(payload.Approval.Reason)
	if reason == "" {
		reason = string(TransportThreadInterruptKindCancelInput)
	}
	consumed := compactConsumedInputsFromIDs(payload.ConsumedMessageIDs)
	t.emitAgentEvent(ctx, Event{
		ID:       t.eventID(payload.RunID),
		TS:       time.Now(),
		ThreadID: t.ThreadID,
		RunID:    payload.RunID,
		Type:     EventInterrupted,
		Payload: InterruptedPayload{
			Source:       "external",
			InterruptID:  payload.InterruptID,
			CheckpointID: payload.CheckpointID,
			Metadata: map[string]string{
				"kind":   string(TransportThreadInterruptKindCancelInput),
				"reason": reason,
			},
		},
		ConsumedInputs: consumed,
	})
	t.emitAgentEvent(ctx, Event{
		ID:             t.eventID(payload.RunID),
		TS:             time.Now(),
		ThreadID:       t.ThreadID,
		RunID:          payload.RunID,
		Type:           EventRunEnd,
		Payload:        RunEndPayload{Status: "interrupted"},
		ConsumedInputs: consumed,
	})
}

func (t *Thread) postCompact(ctx context.Context, cmd compactCommand) (err error) {
	runID := cmd.runID
	if t.CurrentRun() != nil || t.activeCompact() != nil {
		return ErrThreadRunning
	}

	compactCtx, cancel := context.WithCancel(ctx)
	op := &compactOperation{
		runID:              runID,
		consumedMessageIDs: cmd.consumedMessageIDs,
		consumedInputsMeta: cmd.consumedInputsMeta,
		cancel:             cancel,
		done:               make(chan struct{}),
	}
	if !t.beginCompact(op) {
		cancel()
		return ErrThreadRunning
	}

	defer t.finishCompact(op)
	defer cancel()
	defer func() {
		t.emitAgentEvent(context.WithoutCancel(ctx), Event{ID: t.eventID(runID), TS: time.Now(), ThreadID: t.ThreadID, RunID: runID, Type: EventRunEnd, Payload: RunEndPayload{}, ConsumedInputs: cmd.consumedInputs, ConsumedInputsMeta: cmd.consumedInputsMeta})
	}()

	t.emitAgentEvent(context.WithoutCancel(ctx), Event{
		ID:       t.eventID(runID),
		TS:       time.Now(),
		ThreadID: t.ThreadID,
		RunID:    runID,
		Type:     EventContextCompactStarted,
		Payload: ContextCompactStartedPayload{
			ContextUsage: t.ContextManager().ContextUsage(),
		},
		ConsumedInputs:     cmd.consumedInputs,
		ConsumedInputsMeta: cmd.consumedInputsMeta,
	})
	payload, err := t.conversation.Compact(compactCtx, runID)
	if err != nil {
		if errors.Is(err, ErrThreadRunning) {
			t.emitAgentEvent(context.WithoutCancel(ctx), Event{
				ID:                 t.eventID(runID),
				TS:                 time.Now(),
				ThreadID:           t.ThreadID,
				RunID:              runID,
				Type:               EventError,
				Payload:            ErrorPayload{Message: "compact rejected: thread is running"},
				ConsumedInputs:     cmd.consumedInputs,
				ConsumedInputsMeta: cmd.consumedInputsMeta,
			})
			return nil
		}
		{
			req, ok := t.compactInterruptRequest(op)
			if ok {

				t.emitCompactInterruptedEvent(context.WithoutCancel(ctx), op, req)
				return nil
			}
		}

		t.emitAgentEvent(context.WithoutCancel(ctx), Event{
			ID:                 t.eventID(runID),
			TS:                 time.Now(),
			ThreadID:           t.ThreadID,
			RunID:              runID,
			Type:               EventError,
			Payload:            ErrorPayload{Message: fmt.Sprintf("compact failed: %v", err), Cancelled: errors.Is(err, context.Canceled)},
			ConsumedInputs:     cmd.consumedInputs,
			ConsumedInputsMeta: cmd.consumedInputsMeta,
		})
		return nil
	}
	if payload == nil {
		usage := t.ContextManager().ContextUsage()
		payload = &ContextCompactedPayload{Before: usage, After: usage}
	}

	t.emitAgentEvent(context.WithoutCancel(ctx), Event{
		ID:                 t.eventID(runID),
		TS:                 time.Now(),
		ThreadID:           t.ThreadID,
		RunID:              runID,
		Type:               EventContextCompacted,
		Payload:            *payload,
		ConsumedInputs:     cmd.consumedInputs,
		ConsumedInputsMeta: cmd.consumedInputsMeta,
	})
	return nil
}

func (t *Thread) beginCompact(op *compactOperation) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed || t.compact != nil || t.current != nil {
		return false
	}
	t.compact = op
	return true
}

func (t *Thread) finishCompact(op *compactOperation) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.compact == op {
		t.compact = nil
		close(op.done)
	}
}

func (t *Thread) activeCompact() *compactOperation {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.compact == nil {
		return nil
	}
	copy := *t.compact
	copy.consumedMessageIDs = append([]string(nil), t.compact.consumedMessageIDs...)
	copy.consumedInputsMeta = append([]any(nil), t.compact.consumedInputsMeta...)
	return &copy
}

func (t *Thread) interruptCompact(_ context.Context, req TransportThreadInterruptRequest) bool {
	t.mu.Lock()
	op := t.compact
	if op == nil {
		t.mu.Unlock()
		return false
	}
	if op.interrupted {
		t.mu.Unlock()
		return true
	}
	op.interrupted = true
	op.interrupt = req
	cancel := op.cancel
	t.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return true
}

func (t *Thread) compactInterruptRequest(op *compactOperation) (TransportThreadInterruptRequest, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.compact != op || !op.interrupted {
		return TransportThreadInterruptRequest{}, false
	}
	return op.interrupt, true
}

func (t *Thread) emitCompactInterruptedEvent(ctx context.Context, op *compactOperation, req TransportThreadInterruptRequest) {
	if op == nil {
		return
	}
	t.emitAgentEvent(ctx, Event{
		ID:                 t.eventID(op.runID),
		TS:                 time.Now(),
		ThreadID:           t.ThreadID,
		RunID:              op.runID,
		Type:               agentEventContextCompactInterrupted,
		Payload:            newContextCompactInterruptedPayload(req),
		ConsumedInputs:     compactConsumedInputsFromIDs(op.consumedMessageIDs),
		ConsumedInputsMeta: op.consumedInputsMeta,
	})
}

func (t *Thread) runOutputBridge(ctx context.Context, bridge *threadOutputBridge) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-bridge.finish:
			// The owning Core has stopped producing. Drain queued events
			// without canceling their delivery before closing the output.
			drainCtx := context.WithoutCancel(ctx)
			for {
				select {
				case ev, ok := <-bridge.agentEvents:
					if !ok {
						bridge.agentEvents = nil
						continue
					}
					if !t.forwardAgentEvent(drainCtx, ev) {
						return
					}
				case item := <-bridge.inbox:
					if !bridge.deliver(drainCtx, t, item) {
						return
					}
				default:
					return
				}
			}
		case ev, ok := <-bridge.agentEvents:
			if !ok {
				return
			}
			if !t.forwardAgentEvent(ctx, ev) {
				return
			}
		case item := <-bridge.inbox:
			if !bridge.deliver(ctx, t, item) {
				return
			}
		}
	}
}

func (t *Thread) startThreadOutputObserver(ctx context.Context) {
	if t.threadOutputObserver == nil {
		return
	}
	t.observerOnce.Do(func() {
		t.observerQueue = make(chan ThreadOutputObservation, threadOutputObserverQueueSize)
		observerCtx, cancel := context.WithCancel(ctx)
		t.observerCancel = cancel
		go t.runThreadOutputObserver(observerCtx)
	})
}

func (t *Thread) runThreadOutputObserver(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case obs := <-t.observerQueue:
			t.callThreadOutputObserver(ctx, obs)
		}
	}
}

func (t *Thread) enqueueThreadOutputObservation(ctx context.Context, observation ThreadOutputObservation) {
	select {
	case t.observerQueue <- observation:
	default:

	}
}

func (t *Thread) threadOutputObservation(item TransportThreadOutputItem) (ThreadOutputObservation, bool) {
	if t.threadOutputObserver == nil || t.observerQueue == nil {
		return ThreadOutputObservation{}, false
	}
	return ThreadOutputObservation{
		SessionID: t.sessionID,
		ThreadID:  t.ThreadID,
		Item:      cloneThreadOutputItem(item),
	}, true
}

func (t *Thread) callThreadOutputObserver(ctx context.Context, obs ThreadOutputObservation) {
	defer func() {
		_ = recover()
	}()
	t.threadOutputObserver(ctx, obs)
}

func (t *Thread) forwardAgentEvent(ctx context.Context, ev Event) bool {
	usage := t.ContextManager().ContextUsage()
	item, err := threadOutputItem(t.sessionID, t.ThreadID, ev, &usage)
	if err != nil {
		return t.outputBridge.deliver(ctx, t, TransportThreadOutputItem{Err: err})
	}
	if item == nil {
		return true
	}

	if ev.Type == EventRunEnd && t.runFinishedObserver != nil {
		t.runFinishedObserver(ctx, ev)
	}
	return t.outputBridge.deliver(ctx, t, *item)
}

func (t *Thread) emitAgentEvent(ctx context.Context, ev Event) {
	t.mu.Lock()
	bridge := t.outputBridge
	t.mu.Unlock()
	if bridge == nil {
		return
	}
	usage := t.ContextManager().ContextUsage()
	item, err := threadOutputItem(t.sessionID, t.ThreadID, ev, &usage)
	if err != nil {
		bridge.send(ctx, TransportThreadOutputItem{Err: err})
		return
	}
	if item == nil {
		return
	}

	bridge.send(ctx, *item)
}

func (t *Thread) eventID(runID string) string {
	return fmt.Sprintf("evt_%s_%s_%d", t.ThreadID, runID, time.Now().UnixNano())
}

func (t *Thread) withThreadInfo(ctx context.Context) context.Context {
	return ContextContextWithThreadIdentity(ctx, t.threadInfo)
}

func (t *Thread) ensureOpen() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return TransportErrThreadClosed
	}
	return nil
}

// commands

type userInputCommand struct {
	message *TransportMessage
	input   inputpkg.UserMessage
	schema  *schemapkg.Message
	mode    inputpkg.UserMessageMode
}

type resumeRunCommand struct {
	message *TransportMessage
	payload inputpkg.ResumeRunPayload
	mode    inputpkg.UserMessageMode
}

type compactCommand struct {
	message            *TransportMessage
	runID              string
	consumedMessageIDs []string
	consumedInputs     []*schemapkg.Message
	consumedInputsMeta []any
}

func decodeUserInputCommand(message *TransportMessage) (cmd userInputCommand, err error) {
	input, err := parseUserMessage(message)
	if err != nil {
		return userInputCommand{}, err
	}
	userInput, err := protocolUserMessageToSchemaMessage(input)
	if err != nil {
		return userInputCommand{}, err
	}
	attachAttribute(userInput, attributeFromWorkerMessage(message))
	return userInputCommand{
		message: message,
		input:   input,
		schema:  userInput,
		mode:    userInputMode(message, input.Mode),
	}, nil
}

func decodeResumeRunCommand(message *TransportMessage) (cmd resumeRunCommand, err error) {
	payload, err := parseResumePayload(message)
	if err != nil {
		return resumeRunCommand{}, err
	}
	return resumeRunCommand{
		message: message,
		payload: payload,
		mode:    messageMetadataMode(message),
	}, nil
}

func decodeCompactCommand(message *TransportMessage) compactCommand {
	consumed := compactConsumedMessageIDs(message)
	return compactCommand{
		message:            message,
		runID:              compactRunID(message),
		consumedMessageIDs: consumed,
		consumedInputs:     compactConsumedInputsFromIDs(consumed),
		consumedInputsMeta: compactConsumedInputsMeta(message, consumed),
	}
}

func workerMessageID(message *TransportMessage) (id string) {
	if message == nil {
		return ""
	}
	return message.ID
}

func compactRunID(message *TransportMessage) string {
	if message != nil {
		id := strings.TrimSpace(message.ID)
		if id != "" {
			return "compact_" + id
		}
	}
	return "compact_" + uuid.NewString()
}

func compactConsumedMessageIDs(message *TransportMessage) []string {
	if message == nil || strings.TrimSpace(message.ID) == "" {
		return nil
	}
	return []string{strings.TrimSpace(message.ID)}
}

func compactConsumedInputsFromIDs(ids []string) []*schemapkg.Message {
	if len(ids) == 0 {
		return nil
	}
	out := make([]*schemapkg.Message, 0, len(ids))
	for _, id := range ids {
		msg := schemapkg.SystemMessage("compact")
		attachAttribute(msg, MessageAttribute{MessageID: id})
		out = append(out, msg)
	}
	return out
}

func compactConsumedInputsMeta(message *TransportMessage, consumedMessageIDs []string) []any {
	if message == nil || len(consumedMessageIDs) == 0 || len(message.Metadata) == 0 {
		return nil
	}
	return []any{maps.Clone(message.Metadata)}
}

func unsupportedRuntimeCommand(message *TransportMessage) error {
	if message == nil {
		return fmt.Errorf("worker message is required")
	}
	return fmt.Errorf("unsupported message type: %s", message.Type)
}

// compact event

const agentEventContextCompactInterrupted EventType = "context_compact_interrupted"

type contextCompactInterruptedPayload struct {
	Kind             string
	Reason           string
	ControlMessageID string
	CutoffMessageID  string
}

func newContextCompactInterruptedPayload(req TransportThreadInterruptRequest) contextCompactInterruptedPayload {
	return contextCompactInterruptedPayload{
		Kind:             string(req.Kind),
		Reason:           req.Reason,
		ControlMessageID: req.ControlMessageID,
		CutoffMessageID:  req.CutoffMessageID,
	}
}

// compact runtime

type compactOperation struct {
	done               chan struct{}
	runID              string
	consumedMessageIDs []string
	consumedInputsMeta []any
	cancel             context.CancelFunc
	interrupted        bool
	interrupt          TransportThreadInterruptRequest
}

// event mapper

func agentEventPayloadForOutput(ev Event, usage *ContextUsageSnapshot) (eventType eventpkg.EventType, payload any, err error) {
	defer func() {
		if err == nil && payload != nil {
			err = attachConsumedInputs(payload, ev.ConsumedInputs, ev.ConsumedInputsMeta)
		}
	}()
	contextUsage := convertContextUsagePayload(usage)
	switch ev.Type {
	case EventRunStart:
		payload, err := agentEventPayload[RunStartPayload](ev)
		if err != nil {
			return "", nil, err
		}
		out := messageEventPayloadFromRunStart(payload, ev.ConsumedInputs)
		if out == nil {
			out = &eventpkg.MessageEventPayload{}
		}
		out.Status = eventpkg.RunStatusStarted
		out.ContextUsage = contextUsage
		return eventpkg.EventTypeRunStatus, out, nil
	case EventLLMRequesting:
		return "", nil, nil
	case EventInputConsumed:
		input, err := agentEventPayload[InputConsumedPayload](ev)
		if err != nil {
			return "", nil, err
		}
		out := messageEventPayloadFromRunStart(RunStartPayload{Input: input.Message}, []*schemapkg.Message{input.Message})
		if out == nil {
			out = &eventpkg.MessageEventPayload{}
		}
		return eventpkg.EventTypeInputConsumed, out, nil
	case EventLLMToken:
		payload, err := agentEventPayload[LLMTokenChunk](ev)
		if err != nil {
			return "", nil, err
		}
		return eventpkg.EventTypeAssistantDelta, &eventpkg.AssistantDeltaEventPayload{
			Delta:                payload.Text,
			ThinkingContentDelta: payload.ReasoningText,
			LLMResponseID:        payload.LLMResponseID,
		}, nil
	case EventLLMEnd:
		payload, err := agentEventPayload[LLMEnd](ev)
		if err != nil {
			return "", nil, err
		}
		out := &eventpkg.MessageEventPayload{
			LLMResponseID: payload.LLMResponseID,
			ContextUsage:  contextUsage,
		}
		if payload.Message != nil {
			out.Parts = schemaAssistantMessageToProtocolParts(payload.Message)
			out.ThinkingContent = payload.Message.ReasoningContent
		}
		return eventpkg.EventTypeAssistantMessage, out, nil
	case EventTokens:
		payload, err := agentEventPayload[TokenUsagePayload](ev)
		if err != nil {
			return "", nil, err
		}
		return eventpkg.EventTypeTokens, &eventpkg.TokenUsageEventPayload{PromptTokens: payload.PromptTokens, CompletionTokens: payload.CompletionTokens, TotalTokens: payload.TotalTokens}, nil
	case EventToolStart:
		payload, err := agentEventPayload[ToolStartPayload](ev)
		if err != nil {
			return "", nil, err
		}
		return eventpkg.EventTypeToolCall, &eventpkg.ToolCallEventPayload{
			ToolCallID:    payload.CallID,
			ToolName:      payload.Name,
			ArgumentsJSON: stringPtrIfNotEmpty(payload.Args),
			Status:        eventpkg.ToolCallStatusStarted,
			ContextUsage:  contextUsage,
		}, nil
	case EventToolCallOutputChunk:
		payload, err := agentEventPayload[ToolCallOutputChunkPayload](ev)
		if err != nil {
			return "", nil, err
		}
		return eventpkg.EventTypeToolCall, &eventpkg.ToolCallEventPayload{
			ToolCallID:  payload.CallID,
			ToolName:    payload.Name,
			Status:      eventpkg.ToolCallStatusStarted,
			OutputDelta: stringPtrIfNotEmpty(payload.Chunk),
		}, nil
	case EventToolEnd:
		payload, err := agentEventPayload[ToolEndPayload](ev)
		if err != nil {
			return "", nil, err
		}
		out := &eventpkg.ToolCallEventPayload{
			ToolCallID:    payload.CallID,
			ToolName:      payload.Name,
			ArgumentsJSON: stringPtrIfNotEmpty(payload.ArgumentsInJSON),
			ResultJSON:    stringPtrIfNotEmpty(payload.Result),
			Status:        eventpkg.ToolCallStatusFinished,
			ContextUsage:  contextUsage,
		}
		if !payload.ToolStartTime.IsZero() && !ev.TS.IsZero() && ev.TS.After(payload.ToolStartTime) {
			elapsed := ev.TS.Sub(payload.ToolStartTime).Milliseconds()
			out.ElapsedMs = &elapsed
		}
		return eventpkg.EventTypeToolCall, out, nil
	case EventPlanUpdated:
		payload, err := agentEventPayload[PlanUpdatedPayload](ev)
		if err != nil {
			return "", nil, err
		}
		out := planUpdatedPayload(payload)
		out.ContextUsage = contextUsage
		return eventpkg.EventTypePlanUpdated, out, nil
	case EventContextCompactStarted:
		payload, err := agentEventPayload[ContextCompactStartedPayload](ev)
		if err != nil {
			return "", nil, err
		}
		compactUsage := convertContextUsagePayload(&payload.ContextUsage)
		if payload.ContextUsage == (ContextUsageSnapshot{}) {
			compactUsage = contextUsage
		}
		return eventpkg.EventTypeRunStatus, &eventpkg.CompactStartedEventPayload{Status: eventpkg.RunStatusCompactStarted, ContextUsage: compactUsage}, nil
	case EventContextCompacted:
		payload, err := agentEventPayload[ContextCompactedPayload](ev)
		if err != nil {
			return "", nil, err
		}
		return eventpkg.EventTypeRunStatus, &eventpkg.ContextCompactedEventPayload{Status: eventpkg.RunStatusContextCompacted, ContextUsage: convertContextUsagePayload(&payload.After)}, nil
	case agentEventContextCompactInterrupted:
		payload, err := agentEventPayload[contextCompactInterruptedPayload](ev)
		if err != nil {
			return "", nil, err
		}
		return eventpkg.EventTypeRunStatus, &eventpkg.CompactInterruptedEventPayload{
			Status:           eventpkg.RunStatusCompactInterrupted,
			Kind:             payload.Kind,
			Reason:           payload.Reason,
			ControlMessageID: payload.ControlMessageID,
			CutoffMessageID:  payload.CutoffMessageID,
		}, nil
	case EventApproveRequested:
		payload, err := agentEventPayload[ApprovalRequiredPayload](ev)
		if err != nil {
			return "", nil, err
		}
		return eventpkg.EventTypeInputRequired, convertApprovalRequiredPayload(payload), nil
	case EventInterruptBatchRequested:
		batch, err := agentEventPayload[InterruptBatchPayload](ev)
		if err != nil {
			return "", nil, err
		}
		if len(batch.Items) == 0 || batch.CheckpointID == "" {
			return "", nil, fmt.Errorf("interrupt batch lacks items or checkpoint ID")
		}
		out := &eventpkg.InterruptBatchRequiredEventPayload{Kind: eventpkg.InputRequiredKindBatch, InterruptID: batch.Items[0].InterruptID, CheckpointID: batch.CheckpointID}
		for _, item := range batch.Items {
			if item.InterruptID == "" {
				return "", nil, fmt.Errorf("interrupt batch contains an item without ID")
			}
			entry := eventpkg.InterruptBatchItem{Kind: string(item.Kind), InterruptID: item.InterruptID, InfoType: item.InfoType}
			switch {
			case item.ApprovalInfo != nil:
				entry.ToolCallID = item.ApprovalInfo.CallID
				entry.ToolName = item.ApprovalInfo.ToolName
				entry.ArgumentsJSON = stringPtrIfNotEmpty(item.ApprovalInfo.Arguments)
			case item.FollowUpInfo != nil:
				entry.Info, err = json.Marshal(struct {
					Question  string   `json:"question,omitempty"`
					Questions []string `json:"questions,omitempty"`
				}{Question: item.FollowUpInfo.Question, Questions: item.FollowUpInfo.Questions})
				if err != nil {
					return "", nil, err
				}
			default:
				entry.Info, err = json.Marshal(item.Info)
				if err != nil {
					return "", nil, err
				}
			}
			out.Items = append(out.Items, entry)
		}
		return eventpkg.EventTypeInputRequired, out, nil
	case EventFollowUpRequested:
		payload, err := agentEventPayload[FollowUpRequestedPayload](ev)
		if err != nil {
			return "", nil, err
		}
		return eventpkg.EventTypeInputRequired, followUpRequiredPayload(payload), nil
	case EventInterrupted:
		payload, err := agentEventPayload[InterruptedPayload](ev)
		if err != nil {
			return "", nil, err
		}
		if isExternalInterrupt(payload) {
			return eventpkg.EventTypeRunStatus, &eventpkg.ErrorEventPayload{Status: eventpkg.RunStatusInterrupted, Message: interruptedMessage(payload), ContextUsage: contextUsage}, nil
		}
		{
			info, ok := payload.Info.(*coretypes.RequestUserInputInfo)
			if ok {
				return eventpkg.EventTypeInputRequired, planInputRequiredPayload(payload, info), nil
			}
		}
		if isRecoverableRuntimeInterrupt(payload) {
			out, err := interruptRequiredPayload(payload)
			if err != nil {
				return "", nil, err
			}
			return eventpkg.EventTypeInputRequired, out, nil
		}
		return eventpkg.EventTypeRunStatus, &eventpkg.ErrorEventPayload{Status: eventpkg.RunStatusInterrupted, Message: interruptedMessage(payload), ContextUsage: contextUsage}, nil
	case EventRunEnd:
		end, err := agentEventPayload[RunEndPayload](ev)
		if err != nil {
			return "", nil, err
		}
		status := end.Status
		if status == "" {
			status = eventpkg.RunStatusFinished
		}
		switch status {
		case eventpkg.RunStatusBlocked:
			if end.CheckpointID == "" || end.InterruptID == "" {
				return "", nil, errors.New("blocked run lacks checkpoint or interrupt ID")
			}
		case eventpkg.RunStatusFinished, eventpkg.RunStatusInterrupted, eventpkg.RunStatusFailed:
		default:
			return "", nil, fmt.Errorf("unknown run end status %q", status)
		}
		return eventpkg.EventTypeRunStatus, &eventpkg.RunFinishedEventPayload{
			Status: status, CheckpointID: end.CheckpointID, InterruptID: end.InterruptID,
			ContextUsage: contextUsage,
		}, nil
	case EventError:
		out := convertErrorPayload(ev.Payload)
		out.ContextUsage = contextUsage
		return eventpkg.EventTypeError, out, nil
	default:
		return "", nil, nil
	}
}

func attachConsumedInputs(payload any, inputs []*schemapkg.Message, meta []any) (err error) {
	consumed := ConsumedMessageIDs(inputs)
	copied, err := copyConsumedInputMetadata(meta)
	if len(consumed) == 0 && len(copied) == 0 {
		return err
	}
	switch p := payload.(type) {
	case *eventpkg.MessageEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *eventpkg.TokenUsageEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *eventpkg.AssistantDeltaEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *eventpkg.ToolCallEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *eventpkg.ApprovalRequiredEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *eventpkg.InterruptBatchRequiredEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *eventpkg.InterruptRequiredEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *eventpkg.PlanUpdatedEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *eventpkg.PlanInputRequiredEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *eventpkg.CompactStartedEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *eventpkg.ContextCompactedEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *eventpkg.ErrorEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *eventpkg.CompactInterruptedEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *eventpkg.RunFinishedEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	}
	return err
}

func copyConsumedInputMetadata(meta []any) ([]map[string]string, error) {
	if len(meta) == 0 {
		return nil, nil
	}
	out := make([]map[string]string, len(meta))
	hasValue := false
	for i, item := range meta {
		if item == nil {
			continue
		}
		typed, ok := item.(map[string]string)
		if !ok {
			return nil, fmt.Errorf("consumed input metadata at index %d has type %T, want map[string]string", i, item)
		}
		hasValue = true
		out[i] = maps.Clone(typed)
	}
	if !hasValue {
		return nil, nil
	}
	return out, nil
}

func agentEventPayload[T any](ev Event) (T, error) {
	payload, ok := ev.Payload.(T)
	if ok {
		return payload, nil
	}
	var zero T
	return zero, fmt.Errorf("%s payload type mismatch: %T", ev.Type, ev.Payload)
}

func planUpdatedPayload(payload PlanUpdatedPayload) *eventpkg.PlanUpdatedEventPayload {
	items := make([]*eventpkg.PlanItem, len(payload.Plan))
	for i, step := range payload.Plan {
		id := strconv.Itoa(i + 1)
		items[i] = &eventpkg.PlanItem{
			ID:      id,
			Content: step.Step,
			Status:  string(step.Status),
		}
	}
	return &eventpkg.PlanUpdatedEventPayload{
		Explanation: stringPtrIfNotEmpty(payload.Explanation),
		Items:       items,
	}
}

func planInputRequiredPayload(payload InterruptedPayload, info *coretypes.RequestUserInputInfo) *eventpkg.PlanInputRequiredEventPayload {
	if info == nil {
		return nil
	}
	questions := make([]*eventpkg.PlanInputQuestion, len(info.Questions))
	for i, question := range info.Questions {
		options := make([]*eventpkg.PlanInputQuestionOption, len(question.Options))
		for j, option := range question.Options {
			options[j] = &eventpkg.PlanInputQuestionOption{
				Label:       option.Label,
				Description: option.Description,
			}
		}
		questions[i] = &eventpkg.PlanInputQuestion{
			ID:       question.ID,
			Header:   question.Header,
			Question: question.Question,
			Options:  options,
		}
	}
	return &eventpkg.PlanInputRequiredEventPayload{
		Kind:         eventpkg.InputRequiredKindPlanInput,
		InterruptID:  payload.InterruptID,
		CheckpointID: payload.CheckpointID,
		Questions:    questions,
	}
}

func convertContextUsagePayload(snapshot *ContextUsageSnapshot) *eventpkg.ContextUsage {
	if snapshot == nil {
		return nil
	}
	var ratio *float64
	if snapshot.ContextWindow > 0 && snapshot.CurrentTotal > 0 {
		value := float64(snapshot.CurrentTotal) / float64(snapshot.ContextWindow)
		ratio = &value
	}
	return &eventpkg.ContextUsage{
		UsedTokens:       snapshot.CurrentTotal,
		MaxTokens:        int64PtrIfPositive(snapshot.ContextWindow),
		Ratio:            ratio,
		PromptTokens:     int64PtrIfPositive(snapshot.LastModelPromptTokens),
		CompletionTokens: int64PtrIfPositive(snapshot.LastModelCompletionTokens),
	}
}

func convertApprovalRequiredPayload(payload ApprovalRequiredPayload) *eventpkg.ApprovalRequiredEventPayload {
	out := &eventpkg.ApprovalRequiredEventPayload{
		Kind:         eventpkg.InputRequiredKindApproval,
		InterruptID:  payload.InterruptID,
		CheckpointID: payload.CheckpointID,
	}
	if payload.ApprovalInfo != nil {
		out.ToolCallID = payload.ApprovalInfo.CallID
		out.ToolName = payload.ApprovalInfo.ToolName
		out.ArgumentsJSON = stringPtrIfNotEmpty(payload.ApprovalInfo.Arguments)
		return out
	}

	return out
}

func followUpRequiredPayload(payload FollowUpRequestedPayload) *eventpkg.InterruptRequiredEventPayload {
	info := struct {
		Question  string   `json:"question,omitempty"`
		Questions []string `json:"questions,omitempty"`
	}{}
	if payload.Info != nil {
		info.Question = payload.Info.Question
		info.Questions = append([]string(nil), payload.Info.Questions...)
	}
	raw, _ := json.Marshal(info)
	return &eventpkg.InterruptRequiredEventPayload{
		InterruptID:  payload.InterruptID,
		CheckpointID: payload.CheckpointID,
		Kind:         eventpkg.InputRequiredKindFollowUp,
		InfoType:     fmt.Sprintf("%T", payload.Info),
		Info:         raw,
	}
}

func interruptRequiredPayload(payload InterruptedPayload) (*eventpkg.InterruptRequiredEventPayload, error) {
	raw, err := json.Marshal(payload.Info)
	if err != nil {
		return nil, fmt.Errorf("marshal interrupt info: info_type=%s: %w", payload.InfoType, err)
	}
	return &eventpkg.InterruptRequiredEventPayload{
		InterruptID:  payload.InterruptID,
		CheckpointID: payload.CheckpointID,
		Kind:         interruptKind(payload),
		InfoType:     payload.InfoType,
		Info:         raw,
	}, nil
}

func convertErrorPayload(payload any) *eventpkg.ErrorEventPayload {
	switch p := payload.(type) {
	case ErrorPayload:
		return &eventpkg.ErrorEventPayload{Message: p.Message, Cancelled: p.Cancelled}
	case *ErrorPayload:
		if p == nil {
			return &eventpkg.ErrorEventPayload{}
		}
		return &eventpkg.ErrorEventPayload{Message: p.Message, Cancelled: p.Cancelled}
	default:
		return &eventpkg.ErrorEventPayload{Message: fmt.Sprint(payload)}
	}
}

func interruptedMessage(payload InterruptedPayload) string {
	if payload.Source == "external" && payload.Metadata["kind"] == string(TransportThreadInterruptKindWorkerShutdownTimeout) {
		{
			reason := strings.TrimSpace(payload.Metadata["reason"])
			if reason != "" {
				return reason
			}
		}
		return "worker shutdown timeout"
	}
	if payload.Source != "" {
		return "interrupted by " + payload.Source
	}
	return "interrupted"
}

func isExternalInterrupt(payload InterruptedPayload) bool {
	return payload.Source == "external"
}

func isRecoverableRuntimeInterrupt(payload InterruptedPayload) bool {
	return payload.Source != "external" && payload.InterruptID != "" && payload.CheckpointID != ""
}

func interruptKind(payload InterruptedPayload) string {
	if payload.InfoType != "" {
		return "custom"
	}
	if payload.Source != "" {
		return payload.Source
	}
	return "interrupt"
}

func stringPtrIfNotEmpty(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return &value
}

func int64PtrIfPositive(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	return &value
}

// event output

const metadataAgentEventID = "agent_event_id"

func threadOutputItem(sessionID string, threadID string, ev Event, usage *ContextUsageSnapshot) (item *TransportThreadOutputItem, err error) {
	event, err := workerEvent(sessionID, threadID, ev, usage)
	if err != nil {
		return nil, err
	}
	yield := yieldFromAgentEvent(ev)
	if event == nil && yield == nil {
		return nil, nil
	}
	return &TransportThreadOutputItem{Event: event, Yield: yield}, nil
}

func yieldFromAgentEvent(ev Event) *TransportThreadYield {
	switch ev.Type {
	case EventRunEnd:
		payload, err := agentEventPayload[RunEndPayload](ev)
		if err != nil {
			return &TransportThreadYield{Reason: "interrupted", Err: err}
		}
		switch payload.Status {
		case "", "finished", "failed":
			return &TransportThreadYield{Reason: "finished"}
		case "interrupted":
			return &TransportThreadYield{Reason: "interrupted"}
		case "blocked":
			return &TransportThreadYield{Reason: "blocked"}
		default:
			return &TransportThreadYield{Reason: "interrupted", Err: fmt.Errorf("unknown run end status %q", payload.Status)}
		}
	default:
		return nil
	}
}

func workerEvent(_ string, threadID string, ev Event, usage *ContextUsageSnapshot) (output *TransportEvent, err error) {
	if isHiddenInternalToolEvent(ev) {
		return nil, nil
	}

	if ev.Type == EventLLMRequesting {
		return nil, nil
	}

	eventType, eventPayload, err := agentEventPayloadForOutput(ev, usage)
	if err != nil {
		return nil, err
	}
	if eventPayload == nil {
		return nil, nil
	}
	payload, err := json.Marshal(eventPayload)
	if err != nil {
		return nil, err
	}
	event := &TransportEvent{
		ID:       ev.ID,
		ThreadID: threadID,
		RunID:    ev.RunID,
		Type:     TransportEventType(eventType.String()),
		Payload:  payload,
		Metadata: map[string]string{metadataAgentEventID: ev.ID},
		TS:       ev.TS,
	}
	return event, nil
}

// input message

func parseUserMessage(message *TransportMessage) (inputpkg.UserMessage, error) {
	if message == nil {
		return inputpkg.UserMessage{}, fmt.Errorf("message is required")
	}
	var input inputpkg.UserMessage
	{
		err := json.Unmarshal(message.Payload, &input)
		if err != nil {
			return inputpkg.UserMessage{}, fmt.Errorf("unmarshal user message: %w", err)
		}
	}
	{
		err := input.Validate()
		if err != nil {
			return inputpkg.UserMessage{}, err
		}
	}
	return input, nil
}

// message

const (
	MessageTypeInput     TransportMessageType = TransportMessageType(inputpkg.MessageTypeInput)
	MessageTypeResumeRun TransportMessageType = TransportMessageType(inputpkg.MessageTypeResume)
	MessageTypeCompact   TransportMessageType = TransportMessageType(inputpkg.MessageTypeCompact)

	MetadataRunMode = inputpkg.MetadataRunMode
	RunModePlan     = inputpkg.RunModePlan

	einoMessageAttributeExtraKey = "__cloudagent_message_attribute__"
	legacyMessageIDExtraKey      = "message_id"
)

type MessageAttribute struct {
	MessageID  string `json:"message_id,omitempty"`
	SenderID   string `json:"sender_id,omitempty"`
	SenderType string `json:"sender_type,omitempty"`
}

func init() {
	// MessageAttribute is stored in schema.Message.Extra. Eino checkpoint
	// serialization needs a stable name to restore custom Extra values.
	// Keep the historical names so existing local checkpoints remain readable.
	schemapkg.RegisterName[MessageAttribute]("_cloudagent_message_attribute")
	schemapkg.RegisterName[protocolInputParts]("_cloudagent_input_parts")
	schemapkg.RegisterName[inputpkg.MessagePart]("_cloudagent_input_message_part")
}

func attributeFromWorkerMessage(message *TransportMessage) MessageAttribute {
	if message == nil {
		return MessageAttribute{}
	}
	attr := MessageAttribute{MessageID: strings.TrimSpace(message.ID)}
	if message.Sender != nil {
		attr.SenderID = strings.TrimSpace(message.Sender.ID)
		attr.SenderType = strings.TrimSpace(string(message.Sender.Type))
	}
	return attr
}

func attachAttribute(msg *schemapkg.Message, attr MessageAttribute) {
	if msg == nil || attr.empty() {
		return
	}
	if msg.Extra == nil {
		msg.Extra = make(map[string]any, 1)
	}
	msg.Extra[einoMessageAttributeExtraKey] = attr
}

func attributeFromMessage(msg *schemapkg.Message) MessageAttribute {
	if msg == nil || msg.Extra == nil {
		return MessageAttribute{}
	}
	{
		attr, ok := attributeFromExtraValue(msg.Extra[einoMessageAttributeExtraKey])
		if ok {
			return attr
		}
	}
	{
		messageID, ok := stringIDFromAny(msg.Extra[legacyMessageIDExtraKey])
		if ok {
			return MessageAttribute{MessageID: messageID}
		}
	}
	return MessageAttribute{}
}

func ConsumedMessageIDs(inputs []*schemapkg.Message) []string {
	if len(inputs) == 0 {
		return nil
	}
	ids := make([]string, 0, len(inputs))
	for _, input := range inputs {
		attr := attributeFromMessage(input)
		if attr.MessageID != "" {
			ids = append(ids, attr.MessageID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

// MessageID returns the Manager mailbox identity attached to a user message.
// It is used by durable history storage to make Worker redelivery idempotent.
func MessageID(message *schemapkg.Message) string {
	return attributeFromMessage(message).MessageID
}

func attributeFromExtraValue(raw any) (MessageAttribute, bool) {
	switch v := raw.(type) {
	case MessageAttribute:
		attr := v.normalized()
		return attr, !attr.empty()
	case *MessageAttribute:
		if v == nil {
			return MessageAttribute{}, false
		}
		attr := v.normalized()
		return attr, !attr.empty()
	case map[string]any:
		attr := MessageAttribute{}
		{
			messageID, ok := stringIDFromAny(v["message_id"])
			if ok {
				attr.MessageID = messageID
			}
		}
		{
			senderID, ok := stringIDFromAny(v["sender_id"])
			if ok {
				attr.SenderID = senderID
			}
		}
		{
			senderType, ok := stringIDFromAny(v["sender_type"])
			if ok {
				attr.SenderType = senderType
			}
		}
		attr = attr.normalized()
		return attr, !attr.empty()
	default:
		return MessageAttribute{}, false
	}
}

func (a MessageAttribute) normalized() MessageAttribute {
	return MessageAttribute{
		MessageID:  strings.TrimSpace(a.MessageID),
		SenderID:   strings.TrimSpace(a.SenderID),
		SenderType: strings.TrimSpace(a.SenderType),
	}
}

func (a MessageAttribute) empty() bool {
	a = a.normalized()
	return a.MessageID == "" && a.SenderID == "" && a.SenderType == ""
}

func stringIDFromAny(raw any) (string, bool) {
	switch v := raw.(type) {
	case string:
		id := strings.TrimSpace(v)
		return id, id != ""
	case int64:
		if v == 0 {
			return "", false
		}
		return strconv.FormatInt(v, 10), true
	case int:
		if v == 0 {
			return "", false
		}
		return strconv.FormatInt(int64(v), 10), true
	case int32:
		if v == 0 {
			return "", false
		}
		return strconv.FormatInt(int64(v), 10), true
	case float64:
		if v == 0 {
			return "", false
		}
		return strconv.FormatInt(int64(v), 10), true
	case json.Number:
		{
			n, err := v.Int64()
			if err == nil && n != 0 {
				return strconv.FormatInt(n, 10), true
			}
		}
		return "", false
	default:
		return "", false
	}
}

// message codec

const protocolInputPartsExtraKey = "__cloudagent_input_parts__"

type protocolInputParts []inputpkg.MessagePart

func protocolUserMessageToSchemaMessage(input inputpkg.UserMessage) (*schemapkg.Message, error) {
	originalParts := normalizeProtocolInputParts(input.Parts)
	parts := make([]schemapkg.MessageInputPart, 0, len(originalParts))
	text := make([]string, 0, len(originalParts))
	hasNonTextPart := false
	for i, part := range originalParts {
		einoPart, err := protocolPartToSchemaInputPart(part)
		if err != nil {
			return nil, fmt.Errorf("parts[%d]: %w", i, err)
		}
		parts = append(parts, einoPart)
		if part.Type == inputpkg.MessagePartTypeText {
			text = append(text, part.Text)
		} else {
			hasNonTextPart = true
		}
	}

	msg := &schemapkg.Message{Role: schemapkg.User, Extra: protocolExtraToSchemaExtra(input.Extra)}
	if hasNonTextPart {
		msg.UserInputMultiContent = parts
	} else {
		msg.Content = strings.Join(text, "\n")
	}
	attachOriginalProtocolInputParts(msg, originalParts)
	return msg, nil
}

func protocolPartToSchemaInputPart(part inputpkg.MessagePart) (schemapkg.MessageInputPart, error) {
	switch part.Type {
	case inputpkg.MessagePartTypeText:
		return schemapkg.MessageInputPart{
			Type:  schemapkg.ChatMessagePartTypeText,
			Text:  strings.TrimSpace(part.Text),
			Extra: protocolExtraToSchemaExtra(part.Extra),
		}, nil
	case inputpkg.MessagePartTypeImage:
		return schemapkg.MessageInputPart{
			Type:  schemapkg.ChatMessagePartTypeImageURL,
			Image: &schemapkg.MessageInputImage{MessagePartCommon: schemaMessagePartCommon(part), Detail: schemapkg.ImageURLDetail(strings.TrimSpace(part.Detail))},
			Extra: protocolExtraToSchemaExtra(part.Extra),
		}, nil
	case inputpkg.MessagePartTypeAudio:
		return schemapkg.MessageInputPart{
			Type:  schemapkg.ChatMessagePartTypeAudioURL,
			Audio: &schemapkg.MessageInputAudio{MessagePartCommon: schemaMessagePartCommon(part)},
			Extra: protocolExtraToSchemaExtra(part.Extra),
		}, nil
	case inputpkg.MessagePartTypeVideo:
		return schemapkg.MessageInputPart{
			Type:  schemapkg.ChatMessagePartTypeVideoURL,
			Video: &schemapkg.MessageInputVideo{MessagePartCommon: schemaMessagePartCommon(part)},
			Extra: protocolExtraToSchemaExtra(part.Extra),
		}, nil
	case inputpkg.MessagePartTypeFile:
		return schemapkg.MessageInputPart{
			Type:  schemapkg.ChatMessagePartTypeFileURL,
			File:  &schemapkg.MessageInputFile{MessagePartCommon: schemaMessagePartCommon(part), Name: strings.TrimSpace(part.Name)},
			Extra: protocolExtraToSchemaExtra(part.Extra),
		}, nil
	default:
		return schemapkg.MessageInputPart{}, fmt.Errorf("unsupported part type: %s", part.Type)
	}
}

func schemaUserMessageToProtocolParts(message *schemapkg.Message) []eventpkg.MessagePart {
	{
		parts := originalProtocolInputParts(message)
		if len(parts) > 0 {
			return inputPartsForEvent(parts)
		}
	}
	if message == nil {
		return nil
	}
	if len(message.UserInputMultiContent) == 0 {
		return textParts(message.Content)
	}
	parts := make([]eventpkg.MessagePart, 0, len(message.UserInputMultiContent))
	hasText := false
	for _, part := range message.UserInputMultiContent {
		converted, ok := schemaInputPartToProtocolPart(part)
		if ok {
			parts = append(parts, converted)
			hasText = hasText || converted.Type == eventpkg.MessagePartTypeText
		}
	}
	if !hasText && message.Content != "" {
		parts = append(textParts(message.Content), parts...)
	}
	return parts
}

func schemaInputPartToProtocolPart(part schemapkg.MessageInputPart) (eventpkg.MessagePart, bool) {
	switch part.Type {
	case schemapkg.ChatMessagePartTypeText:
		text := strings.TrimSpace(part.Text)
		if text == "" {
			return eventpkg.MessagePart{}, false
		}
		return eventpkg.MessagePart{Type: eventpkg.MessagePartTypeText, Text: text, Extra: schemaExtraToProtocolExtra(part.Extra)}, true
	case schemapkg.ChatMessagePartTypeImageURL:
		out := protocolPartFromCommon(eventpkg.MessagePartTypeImage, messageInputImageCommon(part.Image))
		out.Extra = mergeProtocolExtra(schemaExtraToProtocolExtra(part.Extra), out.Extra)
		if part.Image != nil {
			out.Detail = string(part.Image.Detail)
		}
		return out, true
	case schemapkg.ChatMessagePartTypeAudioURL:
		out := protocolPartFromCommon(eventpkg.MessagePartTypeAudio, messageInputAudioCommon(part.Audio))
		out.Extra = mergeProtocolExtra(schemaExtraToProtocolExtra(part.Extra), out.Extra)
		return out, true
	case schemapkg.ChatMessagePartTypeVideoURL:
		out := protocolPartFromCommon(eventpkg.MessagePartTypeVideo, messageInputVideoCommon(part.Video))
		out.Extra = mergeProtocolExtra(schemaExtraToProtocolExtra(part.Extra), out.Extra)
		return out, true
	case schemapkg.ChatMessagePartTypeFileURL:
		out := protocolPartFromCommon(eventpkg.MessagePartTypeFile, messageInputFileCommon(part.File))
		out.Extra = mergeProtocolExtra(schemaExtraToProtocolExtra(part.Extra), out.Extra)
		if part.File != nil {
			out.Name = part.File.Name
		}
		return out, true
	default:
		return eventpkg.MessagePart{}, false
	}
}

func schemaAssistantMessageToProtocolParts(message *schemapkg.Message) []eventpkg.MessagePart {
	if message == nil {
		return nil
	}
	if len(message.AssistantGenMultiContent) > 0 {
		parts := make([]eventpkg.MessagePart, 0, len(message.AssistantGenMultiContent))
		for _, part := range message.AssistantGenMultiContent {
			converted, ok := schemaOutputPartToProtocolPart(part)
			if ok {
				parts = append(parts, converted)
			}
		}
		if len(parts) > 0 {
			return parts
		}
	}
	return textParts(message.Content)
}

func schemaOutputPartToProtocolPart(part schemapkg.MessageOutputPart) (eventpkg.MessagePart, bool) {
	switch part.Type {
	case schemapkg.ChatMessagePartTypeText:
		return eventpkg.MessagePart{Type: eventpkg.MessagePartTypeText, Text: part.Text, Extra: schemaExtraToProtocolExtra(part.Extra)}, true
	case schemapkg.ChatMessagePartTypeImageURL:
		out := protocolPartFromCommon(eventpkg.MessagePartTypeImage, messageOutputImageCommon(part.Image))
		out.Extra = mergeProtocolExtra(schemaExtraToProtocolExtra(part.Extra), out.Extra)
		return out, true
	case schemapkg.ChatMessagePartTypeAudioURL:
		out := protocolPartFromCommon(eventpkg.MessagePartTypeAudio, messageOutputAudioCommon(part.Audio))
		out.Extra = mergeProtocolExtra(schemaExtraToProtocolExtra(part.Extra), out.Extra)
		return out, true
	case schemapkg.ChatMessagePartTypeVideoURL:
		out := protocolPartFromCommon(eventpkg.MessagePartTypeVideo, messageOutputVideoCommon(part.Video))
		out.Extra = mergeProtocolExtra(schemaExtraToProtocolExtra(part.Extra), out.Extra)
		return out, true
	default:
		return eventpkg.MessagePart{Type: eventpkg.MessagePartType(strings.TrimSuffix(string(part.Type), "_url")), Extra: schemaExtraToProtocolExtra(part.Extra)}, true
	}
}

func attachOriginalProtocolInputParts(msg *schemapkg.Message, parts []inputpkg.MessagePart) {
	if msg == nil || len(parts) == 0 {
		return
	}
	if msg.Extra == nil {
		msg.Extra = make(map[string]any, 1)
	}
	msg.Extra[protocolInputPartsExtraKey] = protocolInputParts(cloneProtocolInputParts(parts))
}

func originalProtocolInputParts(msg *schemapkg.Message) []inputpkg.MessagePart {
	if msg == nil || msg.Extra == nil {
		return nil
	}
	switch parts := msg.Extra[protocolInputPartsExtraKey].(type) {
	case protocolInputParts:
		return cloneProtocolInputParts([]inputpkg.MessagePart(parts))
	case *protocolInputParts:
		if parts == nil {
			return nil
		}
		return cloneProtocolInputParts([]inputpkg.MessagePart(*parts))
	case []inputpkg.MessagePart:
		return cloneProtocolInputParts(parts)
	default:
		return nil
	}
}

func inputPartsForEvent(parts []inputpkg.MessagePart) []eventpkg.MessagePart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]eventpkg.MessagePart, 0, len(parts))
	for _, part := range parts {
		cloned := cloneProtocolInputPart(part)
		out = append(out, eventpkg.MessagePart{
			Type: eventpkg.MessagePartType(cloned.Type), Text: cloned.Text,
			URL: cloned.URL, MIMEType: cloned.MIMEType, Base64Data: cloned.Base64Data,
			Detail: cloned.Detail, Name: cloned.Name, Extra: cloned.Extra,
		})
	}
	return out
}

func normalizeProtocolInputParts(parts []inputpkg.MessagePart) []inputpkg.MessagePart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]inputpkg.MessagePart, len(parts))
	for i, part := range parts {
		out[i] = inputpkg.MessagePart{
			Type:       part.Type,
			Text:       strings.TrimSpace(part.Text),
			URL:        strings.TrimSpace(part.URL),
			Base64Data: strings.TrimSpace(part.Base64Data),
			MIMEType:   strings.TrimSpace(part.MIMEType),
			Name:       strings.TrimSpace(part.Name),
			Detail:     strings.TrimSpace(part.Detail),
			Extra:      cloneProtocolExtra(part.Extra),
		}
	}
	return out
}

func cloneProtocolInputParts(parts []inputpkg.MessagePart) []inputpkg.MessagePart {
	if len(parts) == 0 {
		return nil
	}
	out := make([]inputpkg.MessagePart, len(parts))
	for i, part := range parts {
		out[i] = cloneProtocolInputPart(part)
	}
	return out
}

func schemaMessagePartCommon(part inputpkg.MessagePart) schemapkg.MessagePartCommon {
	common := schemapkg.MessagePartCommon{
		MIMEType: strings.TrimSpace(part.MIMEType),
		Extra:    protocolExtraToSchemaExtra(part.Extra),
	}
	{
		url := strings.TrimSpace(part.URL)
		if url != "" {
			common.URL = &url
		}
	}
	{
		data := strings.TrimSpace(part.Base64Data)
		if data != "" {
			common.Base64Data = &data
		}
	}
	return common
}

func protocolPartFromCommon(partType eventpkg.MessagePartType, common schemapkg.MessagePartCommon) eventpkg.MessagePart {
	out := eventpkg.MessagePart{Type: partType, MIMEType: common.MIMEType, Extra: schemaExtraToProtocolExtra(common.Extra)}
	if common.URL != nil {
		out.URL = *common.URL
	}
	if common.Base64Data != nil {
		out.Base64Data = *common.Base64Data
	}
	return out
}

func cloneProtocolInputPart(part inputpkg.MessagePart) inputpkg.MessagePart {
	return inputpkg.MessagePart{
		Type:       part.Type,
		Text:       part.Text,
		URL:        part.URL,
		Base64Data: part.Base64Data,
		MIMEType:   part.MIMEType,
		Name:       part.Name,
		Detail:     part.Detail,
		Extra:      cloneProtocolExtra(part.Extra),
	}
}

func cloneProtocolExtra(in map[string]json.RawMessage) map[string]json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		out[k] = cloneRawMessage(v)
	}
	return out
}

func protocolExtraToSchemaExtra(in map[string]json.RawMessage) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneRawMessage(v)
	}
	return out
}

func schemaExtraToProtocolExtra(in map[string]any) map[string]json.RawMessage {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for k, v := range in {
		raw, ok := rawMessageFromAny(v)
		if ok {
			out[k] = raw
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func rawMessageFromAny(v any) (json.RawMessage, bool) {
	switch raw := v.(type) {
	case json.RawMessage:
		return cloneRawMessage(raw), true
	case *json.RawMessage:
		if raw == nil {
			return nil, false
		}
		return cloneRawMessage(*raw), true
	case []byte:
		if !json.Valid(raw) {
			break
		}
		return cloneRawMessage(json.RawMessage(raw)), true
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}
	return json.RawMessage(b), true
}

func mergeProtocolExtra(base map[string]json.RawMessage, overrides map[string]json.RawMessage) map[string]json.RawMessage {
	if len(base) == 0 {
		return cloneProtocolExtra(overrides)
	}
	out := cloneProtocolExtra(base)
	for k, v := range overrides {
		out[k] = cloneRawMessage(v)
	}
	return out
}

func cloneRawMessage(in json.RawMessage) json.RawMessage {
	if in == nil {
		return nil
	}
	return append(json.RawMessage(nil), in...)
}

func messageInputImageCommon(part *schemapkg.MessageInputImage) schemapkg.MessagePartCommon {
	if part == nil {
		return schemapkg.MessagePartCommon{}
	}
	return part.MessagePartCommon
}

func messageInputAudioCommon(part *schemapkg.MessageInputAudio) schemapkg.MessagePartCommon {
	if part == nil {
		return schemapkg.MessagePartCommon{}
	}
	return part.MessagePartCommon
}

func messageInputVideoCommon(part *schemapkg.MessageInputVideo) schemapkg.MessagePartCommon {
	if part == nil {
		return schemapkg.MessagePartCommon{}
	}
	return part.MessagePartCommon
}

func messageInputFileCommon(part *schemapkg.MessageInputFile) schemapkg.MessagePartCommon {
	if part == nil {
		return schemapkg.MessagePartCommon{}
	}
	return part.MessagePartCommon
}

func messageOutputImageCommon(part *schemapkg.MessageOutputImage) schemapkg.MessagePartCommon {
	if part == nil {
		return schemapkg.MessagePartCommon{}
	}
	return part.MessagePartCommon
}

func messageOutputAudioCommon(part *schemapkg.MessageOutputAudio) schemapkg.MessagePartCommon {
	if part == nil {
		return schemapkg.MessagePartCommon{}
	}
	return part.MessagePartCommon
}

func messageOutputVideoCommon(part *schemapkg.MessageOutputVideo) schemapkg.MessagePartCommon {
	if part == nil {
		return schemapkg.MessagePartCommon{}
	}
	return part.MessagePartCommon
}

// message mode

func userInputMode(message *TransportMessage, mode inputpkg.UserMessageMode) inputpkg.UserMessageMode {
	if mode != "" {
		return mode
	}
	return messageMetadataMode(message)
}

func messageMetadataMode(message *TransportMessage) inputpkg.UserMessageMode {
	if message == nil || message.Metadata == nil {
		return ""
	}
	switch message.Metadata[MetadataRunMode] {
	case RunModePlan:
		return inputpkg.UserMessageModeImplPlan
	default:
		return ""
	}
}

// threadInterruptMetadata is the metadata forwarded into Run's
// own interrupted event. Compact has its own worker event and does not use this.
func threadInterruptMetadata(req TransportThreadInterruptRequest) map[string]string {
	metadata := map[string]string{}
	if req.Kind != "" {
		metadata["kind"] = string(req.Kind)
	}
	if req.Reason != "" {
		metadata["reason"] = req.Reason
	}
	if req.ControlMessageID != "" {
		metadata["control_message_id"] = req.ControlMessageID
	}
	if req.CutoffMessageID != "" {
		metadata["cutoff_message_id"] = req.CutoffMessageID
	}
	return metadata
}

// message parts

func messageEventPayloadFromRunStart(payload RunStartPayload, consumedInputs []*schemapkg.Message) *eventpkg.MessageEventPayload {
	input := payload.Input
	if input == nil && len(consumedInputs) > 0 {
		input = consumedInputs[0]
	}
	parts := schemaUserMessageToProtocolParts(input)
	attr := attributeFromConsumedInputs(consumedInputs)
	if len(parts) == 0 && attr.empty() {
		return nil
	}
	event := &eventpkg.MessageEventPayload{
		Parts:     parts,
		MessageID: stringPtrIfNotEmpty(attr.MessageID),
	}
	if attr.SenderID != "" || attr.SenderType != "" {
		event.Sender = &eventpkg.Sender{
			SenderType: senderTypeFromString(attr.SenderType),
			SenderID:   attr.SenderID,
		}
	}
	return event
}

func textParts(content string) []eventpkg.MessagePart {
	if content == "" {
		return nil
	}
	return []eventpkg.MessagePart{{Type: "text", Text: content}}
}

func attributeFromConsumedInputs(inputs []*schemapkg.Message) MessageAttribute {
	for _, input := range inputs {
		attr := attributeFromMessage(input)
		if !attr.empty() {
			return attr
		}
	}
	return MessageAttribute{}
}

func senderTypeFromString(senderType string) eventpkg.SenderType {
	switch strings.ToUpper(strings.TrimSpace(senderType)) {
	case "SYSTEM":
		return eventpkg.SenderTypeSystem
	case "AGENT":
		return eventpkg.SenderTypeAgent
	default:
		return eventpkg.SenderTypeUser
	}
}

// output bridge

const threadOutputBridgeBufferSize = 4096

type threadOutputBridge struct {
	agentEvents <-chan Event
	inbox       chan TransportThreadOutputItem
	items       chan TransportThreadOutputItem
	output      *TransportThreadOutput
	done        chan struct{}
	finish      chan struct{}
	finishOnce  sync.Once
}

func (b *threadOutputBridge) start(ctx context.Context, runtime *Thread) *TransportThreadOutput {
	if b.output != nil {
		return b.output
	}
	b.inbox = make(chan TransportThreadOutputItem, threadOutputBridgeBufferSize)
	b.items = make(chan TransportThreadOutputItem, threadOutputBridgeBufferSize)
	b.done = make(chan struct{})
	b.finish = make(chan struct{})
	// Core emits terminal events after cancellation; Close owns bridge lifetime.
	bridgeCtx := context.WithoutCancel(ctx)
	go func(done chan struct{}) {
		defer close(done)
		defer close(b.items)
		runtime.runOutputBridge(bridgeCtx, b)
	}(b.done)
	b.output = &TransportThreadOutput{Items: b.items}
	return b.output
}

func (b *threadOutputBridge) stopAndWait(ctx context.Context) error {
	if b == nil || b.done == nil {
		return nil
	}
	b.finishOnce.Do(func() { close(b.finish) })
	select {
	case <-b.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (b *threadOutputBridge) send(ctx context.Context, item TransportThreadOutputItem) bool {
	if b == nil || b.inbox == nil {
		return false
	}
	done := b.done
	if done != nil {
		select {
		case <-done:
			return false
		default:
		}
	}
	select {
	case b.inbox <- item:
		return true
	case <-done:
		return false
	case <-ctx.Done():
		return false
	}
}

func (b *threadOutputBridge) deliver(ctx context.Context, runtime *Thread, item TransportThreadOutputItem) bool {
	if b == nil || b.items == nil {
		return false
	}
	observation, observe := runtime.threadOutputObservation(item)

	select {
	case b.items <- item:
		if observe {
			runtime.enqueueThreadOutputObservation(ctx, observation)
		}
		return true
	case <-ctx.Done():
		return false
	}
}

// output config

// output observer

const threadOutputObserverQueueSize = 256

func cloneThreadOutputItem(item TransportThreadOutputItem) TransportThreadOutputItem {
	return TransportThreadOutputItem{
		Err:   item.Err,
		Event: cloneWorkerEvent(item.Event),
		Yield: cloneThreadYield(item.Yield),
	}
}

func cloneWorkerEvent(event *TransportEvent) *TransportEvent {
	if event == nil {
		return nil
	}
	clone := *event
	clone.Payload = append([]byte(nil), event.Payload...)
	clone.Metadata = maps.Clone(event.Metadata)
	return &clone
}

func cloneThreadYield(yield *TransportThreadYield) *TransportThreadYield {
	if yield == nil {
		return nil
	}
	clone := *yield
	return &clone
}

// output policy

func isHiddenInternalToolEvent(ev Event) bool {
	switch ev.Type {
	case EventToolStart:
		payload, ok := ev.Payload.(ToolStartPayload)
		return ok && payload.Name == tools.ToolUpdatePlan
	case EventToolCallOutputChunk:
		payload, ok := ev.Payload.(ToolCallOutputChunkPayload)
		return ok && payload.Name == tools.ToolUpdatePlan
	case EventToolEnd:
		payload, ok := ev.Payload.(ToolEndPayload)
		return ok && payload.Name == tools.ToolUpdatePlan
	default:
		return false
	}
}

// resume message

func parseResumePayload(message *TransportMessage) (inputpkg.ResumeRunPayload, error) {
	var payload inputpkg.ResumeRunPayload
	{
		err := json.Unmarshal(message.Payload, &payload)
		if err != nil {
			return inputpkg.ResumeRunPayload{}, fmt.Errorf("unmarshal resume payload: %w", err)
		}
	}
	{
		err := payload.Validate()
		if err != nil {
			return inputpkg.ResumeRunPayload{}, err
		}
	}
	return payload, nil
}

func resumeData(ctx context.Context, payload inputpkg.ResumeRunPayload, interruptResume InterruptResumeDecoder) (map[string]any, error) {
	if len(payload.Answers) > 0 {
		if payload.Answers[0].InterruptID != payload.InterruptID || payload.Approval != nil || payload.RequestUserInput != nil || payload.Interrupt != nil {
			return nil, fmt.Errorf("batch resume correlation or answer format is invalid")
		}
		out := make(map[string]any, len(payload.Answers))
		for _, answer := range payload.Answers {
			if answer.InterruptID == "" {
				return nil, fmt.Errorf("batch answer missing interrupt ID")
			}
			{
				_, exists := out[answer.InterruptID]
				if exists {
					return nil, fmt.Errorf("duplicate batch interrupt ID %q", answer.InterruptID)
				}
			}
			one := inputpkg.ResumeRunPayload{InterruptID: answer.InterruptID, Approval: answer.Approval, RequestUserInput: answer.RequestUserInput, Interrupt: answer.Interrupt}
			value, err := resumeData(ctx, one, interruptResume)
			if err != nil {
				return nil, err
			}
			out[answer.InterruptID] = value[answer.InterruptID]
		}
		return out, nil
	}
	out := map[string]any{}
	switch {
	case payload.Approval != nil:
		out[payload.InterruptID] = approvalResult(payload.Approval)
	case payload.RequestUserInput != nil:
		out[payload.InterruptID] = planInputResponse(payload.RequestUserInput)
	case payload.Interrupt != nil:
		data, err := interruptResumeData(ctx, payload, interruptResume)
		if err != nil {
			return nil, err
		}
		out[payload.InterruptID] = data
	default:
		return nil, fmt.Errorf("resume payload requires approval, request_user_input or interrupt")
	}
	return out, nil
}

func approvalResult(decision *inputpkg.ApprovalDecision) *tools.ApprovalResult {
	if decision == nil {
		return &tools.ApprovalResult{}
	}
	out := &tools.ApprovalResult{Approved: decision.Approved}
	if decision.Reason != "" {
		out.DisapproveReason = &decision.Reason
	}
	return out
}

func planInputResponse(response *inputpkg.RequestUserInputResponse) *coretypes.RequestUserInputResponse {
	if response == nil {
		return nil
	}
	answers := make(map[string]coretypes.RequestUserInputAnswer, len(response.Answers))
	for key, answer := range response.Answers {
		answers[key] = coretypes.RequestUserInputAnswer{Answers: append([]string(nil), answer.Answers...)}
	}
	return &coretypes.RequestUserInputResponse{Answers: answers}
}

func interruptResumeData(ctx context.Context, payload inputpkg.ResumeRunPayload, decoder InterruptResumeDecoder) (any, error) {
	if payload.Interrupt == nil {
		return nil, fmt.Errorf("interrupt resume payload is required")
	}
	switch strings.TrimSpace(payload.Interrupt.Kind) {
	case "follow_up":
		var body struct {
			UserAnswer string `json:"user_answer"`
		}
		{
			err := json.Unmarshal(payload.Interrupt.Data, &body)
			if err != nil {
				return nil, fmt.Errorf("decode follow_up resume data: %w", err)
			}
		}
		answer := strings.TrimSpace(body.UserAnswer)
		if answer == "" {
			return nil, fmt.Errorf("follow_up.user_answer is required")
		}
		return &tools.FollowUpInfo{UserAnswer: answer}, nil
	default:
		if decoder == nil {
			return nil, fmt.Errorf("unsupported interrupt resume kind=%q info_type=%q", payload.Interrupt.Kind, payload.Interrupt.InfoType)
		}
		data, err := decoder(ctx, payload)
		if err != nil {
			return nil, fmt.Errorf("decode interrupt resume kind=%q info_type=%q: %w", payload.Interrupt.Kind, payload.Interrupt.InfoType, err)
		}
		return data, nil
	}
}

// context
// ThreadIdentity is the stable identity of one Run thread. It only holds
// plain values resolved from the Run thread spec; it never carries the
// raw AC thread struct, worker runtime objects, profile, cwd, metadata, UI
// fields or business extension fields.
type ContextThreadIdentity struct {
	ThreadID  string
	SessionID string
	UserID    int64
}

// RunIdentity is the stable identity of one Run run runner execution.
// MessageID identifies the worker message that started or resumed this run so
// observability integrations can correlate a runtime execution with its input.
type ContextRunIdentity struct {
	ThreadID  string
	RunID     string `json:"TurnID" yaml:"turnid"`
	MessageID string
}

type contextThreadInfoKey struct{}

type contextRunInfoKey struct{}

// ContextWithThreadIdentity returns a child context carrying the thread info value.
func ContextContextWithThreadIdentity(ctx context.Context, info ContextThreadIdentity) context.Context {
	return context.WithValue(ctx, contextThreadInfoKey{}, info)
}

// ThreadIdentityFromContext reports the thread info attached to ctx. The bool is false when
// no thread info was attached, so an empty value is not mistaken for a real one.
func ContextThreadIdentityFromContext(ctx context.Context) (ContextThreadIdentity, bool) {
	info, ok := ctx.Value(contextThreadInfoKey{}).(ContextThreadIdentity)
	return info, ok
}

// ContextWithRunIdentity returns a child context carrying the run info value.
func ContextContextWithRunIdentity(ctx context.Context, info ContextRunIdentity) context.Context {
	return context.WithValue(ctx, contextRunInfoKey{}, info)
}

// RunIdentityFromContext reports the run info attached to ctx. The bool is false when no
// run info was attached, so an empty value is not mistaken for a real one.
func ContextRunIdentityFromContext(ctx context.Context) (ContextRunIdentity, bool) {
	info, ok := ctx.Value(contextRunInfoKey{}).(ContextRunIdentity)
	return info, ok
}
