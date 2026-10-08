package thread

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	deeptools "eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"
	eventpkg "eino-cli/deepagent/protocol/event"
	"eino-cli/deepagent/run"
)

var (
	ErrThreadBackpressure = errors.New("agentthread: input queue is full (backpressure)")
	ErrInvalidOp          = errors.New("agentthread: invalid op")
	ErrThreadRunning      = errors.New("agentthread: thread already has an active Run")
	ErrNoActiveRun        = errors.New("agentthread: no active Run")
	ErrRunInputClosed     = errors.New("agentthread: current Run input is closed")
)

func (t *Thread) eventID(runID string) string {
	return fmt.Sprintf("evt_%s_%s_%d", t.ThreadID, runID, time.Now().UnixNano())
}

func agentEventPayloadForOutput(ev run.Event, usage *types.ContextTokenUsage) (eventType eventpkg.EventType, payload any, err error) {
	defer func() {
		if err == nil && payload != nil {
			err = attachConsumedInputs(payload, ev.ConsumedInputs, ev.ConsumedInputsMeta)
		}
	}()
	contextUsage := convertContextUsagePayload(usage)
	switch ev.Type {
	case run.EventRunStart:
		payload, err := agentEventPayload[run.RunStartPayload](ev)
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
	case run.EventLLMRequesting:
		return eventpkg.EventTypeAgentActivity, &eventpkg.AgentActivityEventPayload{Phase: "thinking"}, nil
	case run.EventInputConsumed:
		input, err := agentEventPayload[types.Input](ev)
		if err != nil {
			return "", nil, err
		}
		out := messageEventPayloadFromRunStart(run.RunStartPayload{Input: input.Message}, []*messagepkg.Message{input.Message})
		if out == nil {
			out = &eventpkg.MessageEventPayload{}
		}
		return eventpkg.EventTypeInputConsumed, out, nil
	case run.EventLLMToken:
		payload, err := agentEventPayload[types.LLMTokenChunk](ev)
		if err != nil {
			return "", nil, err
		}
		return eventpkg.EventTypeAssistantDelta, &eventpkg.AssistantDeltaEventPayload{
			Delta:                payload.Text,
			ThinkingContentDelta: payload.ReasoningText,
			LLMResponseID:        payload.LLMResponseID,
		}, nil
	case run.EventLLMEnd:
		payload, err := agentEventPayload[types.LLMEnd](ev)
		if err != nil {
			return "", nil, err
		}
		out := &eventpkg.MessageEventPayload{
			LLMResponseID: payload.LLMResponseID,
			ContextUsage:  contextUsage,
		}
		if payload.Message != nil {
			out.Parts = getAssistantMessageParts(payload.Message)
			out.ThinkingContent = payload.Message.ReasoningContent
		}
		return eventpkg.EventTypeAssistantMessage, out, nil
	case run.EventTokens:
		payload, err := agentEventPayload[types.Usage](ev)
		if err != nil {
			return "", nil, err
		}
		return eventpkg.EventTypeTokens, &eventpkg.TokenUsageEventPayload{PromptTokens: payload.PromptTokens, CompletionTokens: payload.CompletionTokens, TotalTokens: payload.TotalTokens}, nil
	case run.EventToolStart:
		payload, err := agentEventPayload[types.ToolStartPayload](ev)
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
	case run.EventToolCallOutputChunk:
		payload, err := agentEventPayload[types.ToolCallOutputChunkPayload](ev)
		if err != nil {
			return "", nil, err
		}
		return eventpkg.EventTypeToolCall, &eventpkg.ToolCallEventPayload{
			ToolCallID:  payload.CallID,
			ToolName:    payload.Name,
			Status:      eventpkg.ToolCallStatusStarted,
			OutputDelta: stringPtrIfNotEmpty(payload.Chunk),
		}, nil
	case run.EventToolEnd:
		payload, err := agentEventPayload[types.ToolEndPayload](ev)
		if err != nil {
			return "", nil, err
		}
		out := &eventpkg.ToolCallEventPayload{
			ToolCallID:    payload.CallID,
			ToolName:      payload.Name,
			ArgumentsJSON: stringPtrIfNotEmpty(payload.ArgumentsInJSON),
			ResultJSON:    stringPtrIfNotEmpty(payload.Result),
			Parts:         getUserMessageParts(&messagepkg.Message{UserInputMultiContent: payload.MultiContent}),
			IsError:       payload.IsError,
			Status:        eventpkg.ToolCallStatusFinished,
			ContextUsage:  contextUsage,
		}
		if !payload.ToolStartTime.IsZero() && !ev.TS.IsZero() && ev.TS.After(payload.ToolStartTime) {
			elapsed := ev.TS.Sub(payload.ToolStartTime).Milliseconds()
			out.ElapsedMs = &elapsed
		}
		return eventpkg.EventTypeToolCall, out, nil
	case run.EventPlanUpdated:
		payload, err := agentEventPayload[deeptools.PlanUpdate](ev)
		if err != nil {
			return "", nil, err
		}
		out := planUpdatedPayload(payload)
		out.ContextUsage = contextUsage
		return eventpkg.EventTypePlanUpdated, out, nil
	case run.EventContextCompactStarted:
		usage, err := agentEventPayload[types.ContextTokenUsage](ev)
		if err != nil {
			return "", nil, err
		}
		compactUsage := convertContextUsagePayload(&usage)
		if usage == (types.ContextTokenUsage{}) {
			compactUsage = contextUsage
		}
		return eventpkg.EventTypeRunStatus, &eventpkg.CompactStartedEventPayload{Status: eventpkg.RunStatusCompactStarted, ContextUsage: compactUsage}, nil
	case run.EventContextCompacted:
		usage, err := agentEventPayload[types.ContextTokenUsage](ev)
		if err != nil {
			return "", nil, err
		}
		return eventpkg.EventTypeRunStatus, &eventpkg.ContextCompactedEventPayload{Status: eventpkg.RunStatusContextCompacted, ContextUsage: convertContextUsagePayload(&usage)}, nil
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
	case run.EventApproveRequested:
		payload, err := agentEventPayload[run.ApprovalRequiredPayload](ev)
		if err != nil {
			return "", nil, err
		}
		return eventpkg.EventTypeInputRequired, convertApprovalRequiredPayload(payload), nil
	case run.EventInterruptBatchRequested:
		batch, err := agentEventPayload[run.InterruptBatchPayload](ev)
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
	case run.EventFollowUpRequested:
		payload, err := agentEventPayload[run.FollowUpRequestedPayload](ev)
		if err != nil {
			return "", nil, err
		}
		return eventpkg.EventTypeInputRequired, followUpRequiredPayload(payload), nil
	case run.EventInterrupted:
		payload, err := agentEventPayload[run.InterruptedPayload](ev)
		if err != nil {
			return "", nil, err
		}
		if isExternalInterrupt(payload) {
			return eventpkg.EventTypeRunStatus, &eventpkg.ErrorEventPayload{Status: eventpkg.RunStatusInterrupted, Message: interruptedMessage(payload), ContextUsage: contextUsage}, nil
		}
		info, ok := payload.Info.(*types.RequestUserInputInfo)
		if ok {
			return eventpkg.EventTypeInputRequired, planInputRequiredPayload(payload, info), nil
		}
		if isRecoverableRuntimeInterrupt(payload) {
			out, err := interruptRequiredPayload(payload)
			if err != nil {
				return "", nil, err
			}
			return eventpkg.EventTypeInputRequired, out, nil
		}
		return eventpkg.EventTypeRunStatus, &eventpkg.ErrorEventPayload{Status: eventpkg.RunStatusInterrupted, Message: interruptedMessage(payload), ContextUsage: contextUsage}, nil
	case run.EventRunEnd:
		end, err := agentEventPayload[run.RunEndPayload](ev)
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
	case run.EventError:
		out := convertErrorPayload(ev.Payload)
		out.ContextUsage = contextUsage
		return eventpkg.EventTypeError, out, nil
	default:
		return "", nil, nil
	}
}

func attachConsumedInputs(payload any, inputs []*messagepkg.Message, meta []any) (err error) {
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

func agentEventPayload[T any](ev run.Event) (T, error) {
	payload, ok := ev.Payload.(T)
	if ok {
		return payload, nil
	}
	var zero T
	return zero, fmt.Errorf("%s payload type mismatch: %T", ev.Type, ev.Payload)
}

func planUpdatedPayload(payload deeptools.PlanUpdate) *eventpkg.PlanUpdatedEventPayload {
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

func planInputRequiredPayload(payload run.InterruptedPayload, info *types.RequestUserInputInfo) *eventpkg.PlanInputRequiredEventPayload {
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

func convertContextUsagePayload(contextTokenUsage *types.ContextTokenUsage) *eventpkg.ContextUsage {
	if contextTokenUsage == nil {
		return nil
	}
	var ratio *float64
	if contextTokenUsage.MaxContextTokens > 0 && contextTokenUsage.TotalTokens > 0 {
		value := float64(contextTokenUsage.TotalTokens) / float64(contextTokenUsage.MaxContextTokens)
		ratio = &value
	}
	return &eventpkg.ContextUsage{
		UsedTokens:       contextTokenUsage.TotalTokens,
		MaxTokens:        int64PtrIfPositive(contextTokenUsage.MaxContextTokens),
		Ratio:            ratio,
		PromptTokens:     int64PtrIfPositive(contextTokenUsage.PromptTokens),
		CompletionTokens: int64PtrIfPositive(contextTokenUsage.CompletionTokens),
	}
}

func convertApprovalRequiredPayload(payload run.ApprovalRequiredPayload) *eventpkg.ApprovalRequiredEventPayload {
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

func followUpRequiredPayload(payload run.FollowUpRequestedPayload) *eventpkg.InterruptRequiredEventPayload {
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

func interruptRequiredPayload(payload run.InterruptedPayload) (*eventpkg.InterruptRequiredEventPayload, error) {
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
	case run.ErrorPayload:
		return &eventpkg.ErrorEventPayload{Message: p.Message, Cancelled: p.Cancelled}
	case *run.ErrorPayload:
		if p == nil {
			return &eventpkg.ErrorEventPayload{}
		}
		return &eventpkg.ErrorEventPayload{Message: p.Message, Cancelled: p.Cancelled}
	default:
		return &eventpkg.ErrorEventPayload{Message: fmt.Sprint(payload)}
	}
}

func interruptedMessage(payload run.InterruptedPayload) string {
	if payload.Source == "external" && payload.Metadata["kind"] == string(TransportThreadInterruptKindWorkerShutdownTimeout) {
		reason := strings.TrimSpace(payload.Metadata["reason"])
		if reason != "" {
			return reason
		}
		return "worker shutdown timeout"
	}
	if payload.Source != "" {
		return "interrupted by " + payload.Source
	}
	return "interrupted"
}

func isExternalInterrupt(payload run.InterruptedPayload) bool {
	return payload.Source == "external"
}

func isRecoverableRuntimeInterrupt(payload run.InterruptedPayload) bool {
	return payload.Source != "external" && payload.InterruptID != "" && payload.CheckpointID != ""
}

func interruptKind(payload run.InterruptedPayload) string {
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

func messageEventPayloadFromRunStart(payload run.RunStartPayload, consumedInputs []*messagepkg.Message) *eventpkg.MessageEventPayload {
	input := payload.Input
	if input == nil && len(consumedInputs) > 0 {
		input = consumedInputs[0]
	}
	parts := getUserMessageParts(input)
	source := firstIdentifiedInput(consumedInputs)
	if source == nil {
		source = &messagepkg.Message{}
	}
	if len(parts) == 0 && source.MessageID == "" && source.SenderID == "" && source.SenderType == "" {
		return nil
	}
	event := &eventpkg.MessageEventPayload{
		Parts:     parts,
		MessageID: stringPtrIfNotEmpty(source.MessageID),
	}
	if source.SenderID != "" || source.SenderType != "" {
		event.Sender = &eventpkg.Sender{
			SenderType: senderTypeFromString(source.SenderType),
			SenderID:   source.SenderID,
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

func firstIdentifiedInput(inputs []*messagepkg.Message) *messagepkg.Message {
	for _, input := range inputs {
		if input != nil && (input.MessageID != "" || input.SenderID != "" || input.SenderType != "") {
			return input
		}
	}
	return nil
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
