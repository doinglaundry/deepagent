package thread

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	agentmodel "eino-cli/deepagent/model"
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

func agentEventPayloadForOutput(ev agentmodel.RunEvent, usage *agentmodel.ContextTokenUsage) (eventType agentmodel.OutputEventType, payload any, err error) {
	defer func() {
		if err == nil && payload != nil {
			err = attachConsumedInputs(payload, ev.ConsumedInputs, ev.ConsumedInputsMeta)
		}
	}()
	contextUsage := convertContextUsagePayload(usage)
	switch ev.Type {
	case agentmodel.EventRunStart:
		payload, err := agentEventPayload[agentmodel.RunStartPayload](ev)
		if err != nil {
			return "", nil, err
		}
		out := messageEventPayloadFromRunStart(payload, ev.ConsumedInputs)
		if out == nil {
			out = &agentmodel.MessageEventPayload{}
		}
		out.Status = agentmodel.RunStatusStarted
		out.ContextUsage = contextUsage
		return agentmodel.EventTypeRunStatus, out, nil
	case agentmodel.EventLLMRequesting:
		return agentmodel.EventTypeAgentActivity, &agentmodel.AgentActivityEventPayload{Phase: "thinking"}, nil
	case agentmodel.EventInputConsumed:
		input, err := agentEventPayload[agentmodel.RunInput](ev)
		if err != nil {
			return "", nil, err
		}
		out := messageEventPayloadFromRunStart(agentmodel.RunStartPayload{Input: input.Message}, []*agentmodel.Message{input.Message})
		if out == nil {
			out = &agentmodel.MessageEventPayload{}
		}
		return agentmodel.EventTypeInputConsumed, out, nil
	case agentmodel.EventLLMToken:
		payload, err := agentEventPayload[agentmodel.LLMTokenChunk](ev)
		if err != nil {
			return "", nil, err
		}
		return agentmodel.EventTypeAssistantDelta, &agentmodel.AssistantDeltaEventPayload{
			Delta:                payload.Text,
			ThinkingContentDelta: payload.ReasoningText,
			LLMResponseID:        payload.LLMResponseID,
		}, nil
	case agentmodel.EventLLMEnd:
		payload, err := agentEventPayload[agentmodel.LLMEnd](ev)
		if err != nil {
			return "", nil, err
		}
		out := &agentmodel.MessageEventPayload{
			LLMResponseID: payload.LLMResponseID,
			ContextUsage:  contextUsage,
		}
		if payload.Message != nil {
			out.Parts = getAssistantMessageParts(payload.Message)
			out.ThinkingContent = payload.Message.ReasoningContent
		}
		return agentmodel.EventTypeAssistantMessage, out, nil
	case agentmodel.EventTokens:
		payload, err := agentEventPayload[agentmodel.RunUsage](ev)
		if err != nil {
			return "", nil, err
		}
		return agentmodel.EventTypeTokens, &agentmodel.TokenUsageEventPayload{PromptTokens: payload.PromptTokens, CompletionTokens: payload.CompletionTokens, TotalTokens: payload.TotalTokens}, nil
	case agentmodel.EventToolStart:
		payload, err := agentEventPayload[agentmodel.ToolStartPayload](ev)
		if err != nil {
			return "", nil, err
		}
		return agentmodel.EventTypeToolCall, &agentmodel.ToolCallEventPayload{
			ToolCallID:    payload.CallID,
			ToolName:      payload.Name,
			ArgumentsJSON: stringPtrIfNotEmpty(payload.Args),
			Status:        agentmodel.ToolCallStatusStarted,
			ContextUsage:  contextUsage,
		}, nil
	case agentmodel.EventToolCallOutputChunk:
		payload, err := agentEventPayload[agentmodel.ToolCallOutputChunkPayload](ev)
		if err != nil {
			return "", nil, err
		}
		return agentmodel.EventTypeToolCall, &agentmodel.ToolCallEventPayload{
			ToolCallID:  payload.CallID,
			ToolName:    payload.Name,
			Status:      agentmodel.ToolCallStatusStarted,
			OutputDelta: stringPtrIfNotEmpty(payload.Chunk),
		}, nil
	case agentmodel.EventToolEnd:
		payload, err := agentEventPayload[agentmodel.ToolEndPayload](ev)
		if err != nil {
			return "", nil, err
		}
		out := &agentmodel.ToolCallEventPayload{
			ToolCallID:    payload.CallID,
			ToolName:      payload.Name,
			ArgumentsJSON: stringPtrIfNotEmpty(payload.ArgumentsInJSON),
			ResultJSON:    stringPtrIfNotEmpty(payload.Result),
			Parts:         getUserMessageParts(&agentmodel.Message{UserInputMultiContent: payload.MultiContent}),
			IsError:       payload.IsError,
			Status:        agentmodel.ToolCallStatusFinished,
			ContextUsage:  contextUsage,
		}
		if !payload.ToolStartTime.IsZero() && !ev.TS.IsZero() && ev.TS.After(payload.ToolStartTime) {
			elapsed := ev.TS.Sub(payload.ToolStartTime).Milliseconds()
			out.ElapsedMs = &elapsed
		}
		return agentmodel.EventTypeToolCall, out, nil
	case agentmodel.EventPlanUpdated:
		payload, err := agentEventPayload[agentmodel.PlanUpdate](ev)
		if err != nil {
			return "", nil, err
		}
		out := planUpdatedPayload(payload)
		out.ContextUsage = contextUsage
		return agentmodel.EventTypePlanUpdated, out, nil
	case agentmodel.EventContextCompactStarted:
		usage, err := agentEventPayload[agentmodel.ContextTokenUsage](ev)
		if err != nil {
			return "", nil, err
		}
		compactUsage := convertContextUsagePayload(&usage)
		if usage == (agentmodel.ContextTokenUsage{}) {
			compactUsage = contextUsage
		}
		return agentmodel.EventTypeRunStatus, &agentmodel.CompactStartedEventPayload{Status: agentmodel.RunStatusCompactStarted, ContextUsage: compactUsage}, nil
	case agentmodel.EventContextCompacted:
		usage, err := agentEventPayload[agentmodel.ContextTokenUsage](ev)
		if err != nil {
			return "", nil, err
		}
		return agentmodel.EventTypeRunStatus, &agentmodel.ContextCompactedEventPayload{Status: agentmodel.RunStatusContextCompacted, ContextUsage: convertContextUsagePayload(&usage)}, nil
	case agentEventContextCompactInterrupted:
		payload, err := agentEventPayload[contextCompactInterruptedPayload](ev)
		if err != nil {
			return "", nil, err
		}
		return agentmodel.EventTypeRunStatus, &agentmodel.CompactInterruptedEventPayload{
			Status:           agentmodel.RunStatusCompactInterrupted,
			Kind:             payload.Kind,
			Reason:           payload.Reason,
			ControlMessageID: payload.ControlMessageID,
			CutoffMessageID:  payload.CutoffMessageID,
		}, nil
	case agentmodel.EventApproveRequested:
		payload, err := agentEventPayload[agentmodel.ApprovalRequiredPayload](ev)
		if err != nil {
			return "", nil, err
		}
		return agentmodel.EventTypeInputRequired, convertApprovalRequiredPayload(payload), nil
	case agentmodel.EventInterruptBatchRequested:
		batch, err := agentEventPayload[agentmodel.InterruptBatchPayload](ev)
		if err != nil {
			return "", nil, err
		}
		if len(batch.Items) == 0 || batch.CheckpointID == "" {
			return "", nil, fmt.Errorf("interrupt batch lacks items or checkpoint ID")
		}
		out := &agentmodel.InterruptBatchRequiredEventPayload{Kind: agentmodel.InputRequiredKindBatch, InterruptID: batch.Items[0].InterruptID, CheckpointID: batch.CheckpointID}
		for _, item := range batch.Items {
			if item.InterruptID == "" {
				return "", nil, fmt.Errorf("interrupt batch contains an item without ID")
			}
			entry := agentmodel.OutputInterruptBatchItem{Kind: string(item.Kind), InterruptID: item.InterruptID, InfoType: item.InfoType}
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
		return agentmodel.EventTypeInputRequired, out, nil
	case agentmodel.EventFollowUpRequested:
		payload, err := agentEventPayload[agentmodel.FollowUpRequestedPayload](ev)
		if err != nil {
			return "", nil, err
		}
		return agentmodel.EventTypeInputRequired, followUpRequiredPayload(payload), nil
	case agentmodel.EventInterrupted:
		payload, err := agentEventPayload[agentmodel.InterruptedPayload](ev)
		if err != nil {
			return "", nil, err
		}
		if isExternalInterrupt(payload) {
			return agentmodel.EventTypeRunStatus, &agentmodel.ErrorEventPayload{Status: agentmodel.RunStatusInterrupted, Message: interruptedMessage(payload), ContextUsage: contextUsage}, nil
		}
		info, ok := payload.Info.(*agentmodel.RequestUserInputInfo)
		if ok {
			return agentmodel.EventTypeInputRequired, planInputRequiredPayload(payload, info), nil
		}
		if isRecoverableRuntimeInterrupt(payload) {
			out, err := interruptRequiredPayload(payload)
			if err != nil {
				return "", nil, err
			}
			return agentmodel.EventTypeInputRequired, out, nil
		}
		return agentmodel.EventTypeRunStatus, &agentmodel.ErrorEventPayload{Status: agentmodel.RunStatusInterrupted, Message: interruptedMessage(payload), ContextUsage: contextUsage}, nil
	case agentmodel.EventRunEnd:
		end, err := agentEventPayload[agentmodel.RunEndPayload](ev)
		if err != nil {
			return "", nil, err
		}
		status := end.Status
		if status == "" {
			status = agentmodel.RunStatusFinished
		}
		switch status {
		case agentmodel.RunStatusBlocked:
			if end.CheckpointID == "" || end.InterruptID == "" {
				return "", nil, errors.New("blocked run lacks checkpoint or interrupt ID")
			}
		case agentmodel.RunStatusFinished, agentmodel.RunStatusInterrupted, agentmodel.RunStatusFailed:
		default:
			return "", nil, fmt.Errorf("unknown run end status %q", status)
		}
		return agentmodel.EventTypeRunStatus, &agentmodel.RunFinishedEventPayload{
			Status: status, CheckpointID: end.CheckpointID, InterruptID: end.InterruptID,
			ContextUsage: contextUsage,
		}, nil
	case agentmodel.EventError:
		out := convertErrorPayload(ev.Payload)
		out.ContextUsage = contextUsage
		return agentmodel.EventTypeError, out, nil
	default:
		return "", nil, nil
	}
}

func attachConsumedInputs(payload any, inputs []*agentmodel.Message, meta []any) (err error) {
	consumed := ConsumedMessageIDs(inputs)
	copied, err := copyConsumedInputMetadata(meta)
	if len(consumed) == 0 && len(copied) == 0 {
		return err
	}
	switch p := payload.(type) {
	case *agentmodel.MessageEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *agentmodel.TokenUsageEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *agentmodel.AssistantDeltaEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *agentmodel.ToolCallEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *agentmodel.ApprovalRequiredEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *agentmodel.InterruptBatchRequiredEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *agentmodel.InterruptRequiredEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *agentmodel.PlanUpdatedEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *agentmodel.PlanInputRequiredEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *agentmodel.CompactStartedEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *agentmodel.ContextCompactedEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *agentmodel.ErrorEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *agentmodel.CompactInterruptedEventPayload:
		p.ConsumedMessageIDs, p.ConsumedInputsMeta = consumed, copied
	case *agentmodel.RunFinishedEventPayload:
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

func agentEventPayload[T any](ev agentmodel.RunEvent) (T, error) {
	payload, ok := ev.Payload.(T)
	if ok {
		return payload, nil
	}
	var zero T
	return zero, fmt.Errorf("%s payload type mismatch: %T", ev.Type, ev.Payload)
}

func planUpdatedPayload(payload agentmodel.PlanUpdate) *agentmodel.PlanUpdatedEventPayload {
	items := make([]*agentmodel.PlanItem, len(payload.Plan))
	for i, step := range payload.Plan {
		id := strconv.Itoa(i + 1)
		items[i] = &agentmodel.PlanItem{
			ID:      id,
			Content: step.Step,
			Status:  string(step.Status),
		}
	}
	return &agentmodel.PlanUpdatedEventPayload{
		Explanation: stringPtrIfNotEmpty(payload.Explanation),
		Items:       items,
	}
}

func planInputRequiredPayload(payload agentmodel.InterruptedPayload, info *agentmodel.RequestUserInputInfo) *agentmodel.PlanInputRequiredEventPayload {
	if info == nil {
		return nil
	}
	questions := make([]*agentmodel.PlanInputQuestion, len(info.Questions))
	for i, question := range info.Questions {
		options := make([]*agentmodel.PlanInputQuestionOption, len(question.Options))
		for j, option := range question.Options {
			options[j] = &agentmodel.PlanInputQuestionOption{
				Label:       option.Label,
				Description: option.Description,
			}
		}
		questions[i] = &agentmodel.PlanInputQuestion{
			ID:       question.ID,
			Header:   question.Header,
			Question: question.Question,
			Options:  options,
		}
	}
	return &agentmodel.PlanInputRequiredEventPayload{
		Kind:         agentmodel.InputRequiredKindPlanInput,
		InterruptID:  payload.InterruptID,
		CheckpointID: payload.CheckpointID,
		Questions:    questions,
	}
}

func convertContextUsagePayload(contextTokenUsage *agentmodel.ContextTokenUsage) *agentmodel.ContextUsage {
	if contextTokenUsage == nil {
		return nil
	}
	var ratio *float64
	if contextTokenUsage.MaxContextTokens > 0 && contextTokenUsage.TotalTokens > 0 {
		value := float64(contextTokenUsage.TotalTokens) / float64(contextTokenUsage.MaxContextTokens)
		ratio = &value
	}
	return &agentmodel.ContextUsage{
		UsedTokens:       contextTokenUsage.TotalTokens,
		MaxTokens:        int64PtrIfPositive(contextTokenUsage.MaxContextTokens),
		Ratio:            ratio,
		PromptTokens:     int64PtrIfPositive(contextTokenUsage.PromptTokens),
		CompletionTokens: int64PtrIfPositive(contextTokenUsage.CompletionTokens),
	}
}

func convertApprovalRequiredPayload(payload agentmodel.ApprovalRequiredPayload) *agentmodel.ApprovalRequiredEventPayload {
	out := &agentmodel.ApprovalRequiredEventPayload{
		Kind:         agentmodel.InputRequiredKindApproval,
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

func followUpRequiredPayload(payload agentmodel.FollowUpRequestedPayload) *agentmodel.InterruptRequiredEventPayload {
	info := struct {
		Question  string   `json:"question,omitempty"`
		Questions []string `json:"questions,omitempty"`
	}{}
	if payload.Info != nil {
		info.Question = payload.Info.Question
		info.Questions = append([]string(nil), payload.Info.Questions...)
	}
	raw, _ := json.Marshal(info)
	return &agentmodel.InterruptRequiredEventPayload{
		InterruptID:  payload.InterruptID,
		CheckpointID: payload.CheckpointID,
		Kind:         agentmodel.InputRequiredKindFollowUp,
		InfoType:     fmt.Sprintf("%T", payload.Info),
		Info:         raw,
	}
}

func interruptRequiredPayload(payload agentmodel.InterruptedPayload) (*agentmodel.InterruptRequiredEventPayload, error) {
	raw, err := json.Marshal(payload.Info)
	if err != nil {
		return nil, fmt.Errorf("marshal interrupt info: info_type=%s: %w", payload.InfoType, err)
	}
	return &agentmodel.InterruptRequiredEventPayload{
		InterruptID:  payload.InterruptID,
		CheckpointID: payload.CheckpointID,
		Kind:         interruptKind(payload),
		InfoType:     payload.InfoType,
		Info:         raw,
	}, nil
}

func convertErrorPayload(payload any) *agentmodel.ErrorEventPayload {
	switch p := payload.(type) {
	case agentmodel.ErrorPayload:
		return &agentmodel.ErrorEventPayload{Message: p.Message, Cancelled: p.Cancelled}
	case *agentmodel.ErrorPayload:
		if p == nil {
			return &agentmodel.ErrorEventPayload{}
		}
		return &agentmodel.ErrorEventPayload{Message: p.Message, Cancelled: p.Cancelled}
	default:
		return &agentmodel.ErrorEventPayload{Message: fmt.Sprint(payload)}
	}
}

func interruptedMessage(payload agentmodel.InterruptedPayload) string {
	if payload.Source == "external" && payload.Metadata["kind"] == string(agentmodel.TransportThreadInterruptKindWorkerShutdownTimeout) {
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

func isExternalInterrupt(payload agentmodel.InterruptedPayload) bool {
	return payload.Source == "external"
}

func isRecoverableRuntimeInterrupt(payload agentmodel.InterruptedPayload) bool {
	return payload.Source != "external" && payload.InterruptID != "" && payload.CheckpointID != ""
}

func interruptKind(payload agentmodel.InterruptedPayload) string {
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

func messageEventPayloadFromRunStart(payload agentmodel.RunStartPayload, consumedInputs []*agentmodel.Message) *agentmodel.MessageEventPayload {
	input := payload.Input
	if input == nil && len(consumedInputs) > 0 {
		input = consumedInputs[0]
	}
	parts := getUserMessageParts(input)
	source := firstIdentifiedInput(consumedInputs)
	if source == nil {
		source = &agentmodel.Message{}
	}
	if len(parts) == 0 && source.MessageID == "" && source.SenderID == "" && source.SenderType == "" {
		return nil
	}
	event := &agentmodel.MessageEventPayload{
		Parts:     parts,
		MessageID: stringPtrIfNotEmpty(source.MessageID),
	}
	if source.SenderID != "" || source.SenderType != "" {
		event.Sender = &agentmodel.OutputSender{
			SenderType: senderTypeFromString(source.SenderType),
			SenderID:   source.SenderID,
		}
	}
	return event
}

func textParts(content string) []agentmodel.OutputMessagePart {
	if content == "" {
		return nil
	}
	return []agentmodel.OutputMessagePart{{Type: "text", Text: content}}
}

func firstIdentifiedInput(inputs []*agentmodel.Message) *agentmodel.Message {
	for _, input := range inputs {
		if input != nil && (input.MessageID != "" || input.SenderID != "" || input.SenderType != "") {
			return input
		}
	}
	return nil
}

func senderTypeFromString(senderType string) agentmodel.OutputSenderType {
	switch strings.ToUpper(strings.TrimSpace(senderType)) {
	case "SYSTEM":
		return agentmodel.OutputSenderTypeSystem
	case "AGENT":
		return agentmodel.OutputSenderTypeAgent
	default:
		return agentmodel.OutputSenderTypeUser
	}
}
