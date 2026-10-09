package thread

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	agentmodel "eino-cli/deepagent/model"
	"eino-cli/deepagent/run"

	"github.com/google/uuid"
)

func (t *Thread) ResumeRun(ctx context.Context, runID string, opts ResumeRunOptions) (*run.Handle, error) {
	if runID == "" || opts.CheckpointID == "" {
		return nil, ErrInvalidOp
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return nil, ErrInvalidOp
	}
	if t.current != nil || t.compact != nil {
		return nil, ErrThreadRunning
	}
	r, runCtx, err := t.startRun(ctx, RunStartRequest{ThreadID: t.ThreadID, RunID: runID, Resume: &opts}, opts.ConfigProvider, opts.OnRunStart, opts.EnablePlan)
	if err != nil {
		return nil, err
	}
	t.current = r
	t.accepting = true
	go r.Execute(runCtx)
	return r.Handle(), nil
}

func (t *Thread) InterruptRun(opts agentmodel.InterruptOptions) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.current == nil {
		return false
	}
	t.current.RequestInterrupt(opts)
	return true
}

func (t *Thread) Compact(ctx context.Context) (*agentmodel.ContextTokenUsage, error) {
	return t.CompactWithRunID(ctx, uuid.NewString())
}

func (t *Thread) CompactWithRunID(ctx context.Context, runID string) (*agentmodel.ContextTokenUsage, error) {
	if runID == "" {
		return nil, ErrInvalidOp
	}
	compactCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	op := &compactOperation{runID: runID, cancel: cancel, done: make(chan struct{})}
	if !t.beginCompact(op) {
		return nil, ErrThreadRunning
	}
	defer t.finishCompact(op)
	return t.conversation.Compact(compactCtx, runID)
}

func (t *Thread) Interrupt(ctx context.Context, req agentmodel.TransportThreadInterruptRequest) error {
	ctx = t.withThreadInfo(ctx)

	if t.interruptCompact(ctx, req) {
		return nil
	}
	if t.InterruptRun(agentmodel.InterruptOptions{Timeout: req.Timeout, Metadata: threadInterruptMetadata(req)}) {
		return nil
	}
	if t.ActiveRun() == nil {
		return nil
	}
	return fmt.Errorf("interrupt active turn failed: kind=%s control_message_id=%s", req.Kind, req.ControlMessageID)
}

func (t *Thread) postResumeRun(ctx context.Context, cmd resumeRunCommand) (posted *agentmodel.TransportPostMessageResult, err error) {
	payload := cmd.payload
	resumeData, err := t.resumeData(ctx, payload)
	if err != nil {

		return nil, err
	}

	if payload.Approval != nil && !payload.Approval.CancelRun && payload.Approval.AllowInSession && payload.Approval.Approved && t.approvalRemember != nil {
		t.approvalRemember.RememberApproval(ctx, payload)
	}

	resumeIDs := []string{payload.InterruptID}
	if len(payload.Answers) > 0 {
		resumeIDs = resumeIDs[:0]
		for _, answer := range payload.Answers {
			resumeIDs = append(resumeIDs, answer.InterruptID)
		}
	}
	enablePlan := cmd.mode == agentmodel.UserMessageModeImplPlan
	opts := ResumeRunOptions{
		EnablePlan:         &enablePlan,
		CheckpointID:       payload.CheckpointID,
		ResumeInterruptIDs: resumeIDs,
		ResumeData:         resumeData,
		OnRunStart: func(runCtx context.Context, req RunStartRequest) context.Context {
			return agentmodel.ContextContextWithRunIdentity(runCtx, agentmodel.ContextRunIdentity{
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
	return &agentmodel.TransportPostMessageResult{RunID: payload.RunID}, nil
}

func (t *Thread) resumeData(ctx context.Context, payload agentmodel.ResumeRunPayload) (map[string]any, error) {
	return resumeData(ctx, payload, t.interruptResume)
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
		t.emitAgentEvent(context.WithoutCancel(ctx), agentmodel.RunEvent{ID: t.eventID(runID), TS: time.Now(), ThreadID: t.ThreadID, RunID: runID, Type: agentmodel.EventRunEnd, Payload: agentmodel.RunEndPayload{}, ConsumedInputs: cmd.consumedInputs, ConsumedInputsMeta: cmd.consumedInputsMeta})
	}()

	t.emitAgentEvent(context.WithoutCancel(ctx), agentmodel.RunEvent{
		ID:                 t.eventID(runID),
		TS:                 time.Now(),
		ThreadID:           t.ThreadID,
		RunID:              runID,
		Type:               agentmodel.EventContextCompactStarted,
		Payload:            t.ContextManager().GetContextUsage(),
		ConsumedInputs:     cmd.consumedInputs,
		ConsumedInputsMeta: cmd.consumedInputsMeta,
	})
	usage, err := t.conversation.Compact(compactCtx, runID)
	if err != nil {
		if errors.Is(err, ErrThreadRunning) {
			t.emitAgentEvent(context.WithoutCancel(ctx), agentmodel.RunEvent{
				ID:                 t.eventID(runID),
				TS:                 time.Now(),
				ThreadID:           t.ThreadID,
				RunID:              runID,
				Type:               agentmodel.EventError,
				Payload:            agentmodel.ErrorPayload{Message: "compact rejected: thread is running"},
				ConsumedInputs:     cmd.consumedInputs,
				ConsumedInputsMeta: cmd.consumedInputsMeta,
			})
			return nil
		}
		req, ok := t.compactInterruptRequest(op)
		if ok {

			t.emitCompactInterruptedEvent(context.WithoutCancel(ctx), op, req)
			return nil
		}

		t.emitAgentEvent(context.WithoutCancel(ctx), agentmodel.RunEvent{
			ID:                 t.eventID(runID),
			TS:                 time.Now(),
			ThreadID:           t.ThreadID,
			RunID:              runID,
			Type:               agentmodel.EventError,
			Payload:            agentmodel.ErrorPayload{Message: fmt.Sprintf("compact failed: %v", err), Cancelled: errors.Is(err, context.Canceled)},
			ConsumedInputs:     cmd.consumedInputs,
			ConsumedInputsMeta: cmd.consumedInputsMeta,
		})
		return nil
	}
	if usage == nil {
		currentUsage := t.ContextManager().GetContextUsage()
		usage = &currentUsage
	}

	t.emitAgentEvent(context.WithoutCancel(ctx), agentmodel.RunEvent{
		ID:                 t.eventID(runID),
		TS:                 time.Now(),
		ThreadID:           t.ThreadID,
		RunID:              runID,
		Type:               agentmodel.EventContextCompacted,
		Payload:            *usage,
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

func (t *Thread) interruptCompact(_ context.Context, req agentmodel.TransportThreadInterruptRequest) bool {
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

func (t *Thread) compactInterruptRequest(op *compactOperation) (agentmodel.TransportThreadInterruptRequest, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.compact != op || !op.interrupted {
		return agentmodel.TransportThreadInterruptRequest{}, false
	}
	return op.interrupt, true
}

func (t *Thread) emitCompactInterruptedEvent(ctx context.Context, op *compactOperation, req agentmodel.TransportThreadInterruptRequest) {
	if op == nil {
		return
	}
	t.emitAgentEvent(ctx, agentmodel.RunEvent{
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

type userInputCommand struct {
	message      *agentmodel.TransportMessage
	input        agentmodel.UserMessage
	inputMessage *agentmodel.Message
	mode         agentmodel.UserMessageMode
}

type resumeRunCommand struct {
	message *agentmodel.TransportMessage
	payload agentmodel.ResumeRunPayload
	mode    agentmodel.UserMessageMode
}

type compactCommand struct {
	message            *agentmodel.TransportMessage
	runID              string
	consumedMessageIDs []string
	consumedInputs     []*agentmodel.Message
	consumedInputsMeta []any
}

func decodeUserInputCommand(message *agentmodel.TransportMessage) (cmd userInputCommand, err error) {
	input, err := parseUserMessage(message)
	if err != nil {
		return userInputCommand{}, err
	}
	userInput, err := decodeDialogueMessage(input)
	if err != nil {
		return userInputCommand{}, err
	}
	userInput.MessageID = strings.TrimSpace(message.ID)
	if message.Sender != nil {
		userInput.SenderID = strings.TrimSpace(message.Sender.ID)
		userInput.SenderType = strings.TrimSpace(string(message.Sender.Type))
	}
	return userInputCommand{
		message:      message,
		input:        input,
		inputMessage: userInput,
		mode:         userInputMode(message, input.Mode),
	}, nil
}

func decodeResumeRunCommand(message *agentmodel.TransportMessage) (cmd resumeRunCommand, err error) {
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

func decodeCompactCommand(message *agentmodel.TransportMessage) compactCommand {
	consumed := compactConsumedMessageIDs(message)
	return compactCommand{
		message:            message,
		runID:              compactRunID(message),
		consumedMessageIDs: consumed,
		consumedInputs:     compactConsumedInputsFromIDs(consumed),
		consumedInputsMeta: compactConsumedInputsMeta(message, consumed),
	}
}

func workerMessageID(message *agentmodel.TransportMessage) (id string) {
	if message == nil {
		return ""
	}
	return message.ID
}

func compactRunID(message *agentmodel.TransportMessage) string {
	if message != nil {
		id := strings.TrimSpace(message.ID)
		if id != "" {
			return "compact_" + id
		}
	}
	return "compact_" + uuid.NewString()
}

func compactConsumedMessageIDs(message *agentmodel.TransportMessage) []string {
	if message == nil || strings.TrimSpace(message.ID) == "" {
		return nil
	}
	return []string{strings.TrimSpace(message.ID)}
}

func compactConsumedInputsFromIDs(ids []string) []*agentmodel.Message {
	if len(ids) == 0 {
		return nil
	}
	out := make([]*agentmodel.Message, 0, len(ids))
	for _, id := range ids {
		msg := agentmodel.NewSystemMessage("compact")
		msg.MessageID = id
		out = append(out, msg)
	}
	return out
}

func compactConsumedInputsMeta(message *agentmodel.TransportMessage, consumedMessageIDs []string) []any {
	if message == nil || len(consumedMessageIDs) == 0 || len(message.Metadata) == 0 {
		return nil
	}
	return []any{maps.Clone(message.Metadata)}
}

func unsupportedRuntimeCommand(message *agentmodel.TransportMessage) error {
	if message == nil {
		return fmt.Errorf("worker message is required")
	}
	return fmt.Errorf("unsupported message type: %s", message.Type)
}

const agentEventContextCompactInterrupted agentmodel.RunEventType = "context_compact_interrupted"

type contextCompactInterruptedPayload struct {
	Kind             string
	Reason           string
	ControlMessageID string
	CutoffMessageID  string
}

func newContextCompactInterruptedPayload(req agentmodel.TransportThreadInterruptRequest) contextCompactInterruptedPayload {
	return contextCompactInterruptedPayload{
		Kind:             string(req.Kind),
		Reason:           req.Reason,
		ControlMessageID: req.ControlMessageID,
		CutoffMessageID:  req.CutoffMessageID,
	}
}

type compactOperation struct {
	done               chan struct{}
	runID              string
	consumedMessageIDs []string
	consumedInputsMeta []any
	cancel             context.CancelFunc
	interrupted        bool
	interrupt          agentmodel.TransportThreadInterruptRequest
}

// threadInterruptMetadata is the metadata forwarded into Run's
// own interrupted event. Compact has its own worker event and does not use this.
func threadInterruptMetadata(req agentmodel.TransportThreadInterruptRequest) map[string]string {
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

func parseResumePayload(message *agentmodel.TransportMessage) (agentmodel.ResumeRunPayload, error) {
	var payload agentmodel.ResumeRunPayload
	err := json.Unmarshal(message.Payload, &payload)
	if err != nil {
		return agentmodel.ResumeRunPayload{}, fmt.Errorf("unmarshal resume payload: %w", err)
	}
	validationErr := payload.Validate()
	if validationErr != nil {
		return agentmodel.ResumeRunPayload{}, validationErr
	}
	return payload, nil
}

func resumeData(ctx context.Context, payload agentmodel.ResumeRunPayload, interruptResume agentmodel.InterruptResumeDecoder) (map[string]any, error) {
	if len(payload.Answers) > 0 {
		if payload.Answers[0].InterruptID != payload.InterruptID || payload.Approval != nil || payload.RequestUserInput != nil || payload.Interrupt != nil {
			return nil, fmt.Errorf("batch resume correlation or answer format is invalid")
		}
		out := make(map[string]any, len(payload.Answers))
		for _, answer := range payload.Answers {
			if answer.InterruptID == "" {
				return nil, fmt.Errorf("batch answer missing interrupt ID")
			}
			_, exists := out[answer.InterruptID]
			if exists {
				return nil, fmt.Errorf("duplicate batch interrupt ID %q", answer.InterruptID)
			}
			one := agentmodel.ResumeRunPayload{InterruptID: answer.InterruptID, Approval: answer.Approval, RequestUserInput: answer.RequestUserInput, Interrupt: answer.Interrupt}
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

func approvalResult(decision *agentmodel.ApprovalDecision) *agentmodel.ApprovalResult {
	if decision == nil {
		return &agentmodel.ApprovalResult{}
	}
	out := &agentmodel.ApprovalResult{Approved: decision.Approved, CancelRun: decision.CancelRun}
	if decision.Reason != "" {
		out.DisapproveReason = &decision.Reason
	}
	return out
}

func planInputResponse(response *agentmodel.InputRequestUserInputResponse) *agentmodel.RequestUserInputResponse {
	if response == nil {
		return nil
	}
	answers := make(map[string]agentmodel.RequestUserInputAnswer, len(response.Answers))
	for key, answer := range response.Answers {
		answers[key] = agentmodel.RequestUserInputAnswer{Answers: append([]string(nil), answer.Answers...)}
	}
	return &agentmodel.RequestUserInputResponse{Answers: answers}
}

func interruptResumeData(ctx context.Context, payload agentmodel.ResumeRunPayload, decoder agentmodel.InterruptResumeDecoder) (any, error) {
	if payload.Interrupt == nil {
		return nil, fmt.Errorf("interrupt resume payload is required")
	}
	switch strings.TrimSpace(payload.Interrupt.Kind) {
	case "follow_up":
		var body struct {
			UserAnswer string `json:"user_answer"`
		}
		err := json.Unmarshal(payload.Interrupt.Data, &body)
		if err != nil {
			return nil, fmt.Errorf("decode follow_up resume data: %w", err)
		}
		answer := strings.TrimSpace(body.UserAnswer)
		if answer == "" {
			return nil, fmt.Errorf("follow_up.user_answer is required")
		}
		return &agentmodel.FollowUpInfo{UserAnswer: answer}, nil
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
