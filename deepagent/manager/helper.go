package manager

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"eino-cli/deepagent/dal/cache"
	"eino-cli/deepagent/dal/model"
	eventpkg "eino-cli/deepagent/protocol/event"
)

func IDNextSharedID(ctx context.Context, counter cache.RedisClient) (int64, error) {
	if counter == nil {
		return 0, ErrRedisUnavailable
	}
	seq, err := counter.IncrBy(ctx, "deepagent:coordinator:global_id", 1)
	if err != nil {
		return 0, err
	}
	const base int64 = 2_000_000_000_000_000_000
	if seq <= 0 || seq > int64(^uint64(0)>>1)-base {
		return 0, errors.New("distributed ID counter overflow")
	}
	return base + seq, nil
}

func createThread(req SubmitRequest, id int64) *model.Thread {
	metadata := maps.Clone(req.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	{
		title := strings.TrimSpace(req.Title)
		if title != "" {
			metadata["title"] = title
		}
	}
	var profile *model.Profile
	if req.Profile != nil && *req.Profile != (model.Profile{}) {
		copy := *req.Profile
		profile = &copy
	}
	return &model.Thread{ThreadID: id, UserID: req.UserID, SessionID: req.SessionID, Status: model.ThreadStatusOpen, Metadata: metadata, Profile: profile}
}

func normalizeLeaseDuration(ms int64) time.Duration {
	if ms <= 0 {
		return defaultLeaseDuration
	}
	if ms > int64(maxLeaseDuration/time.Millisecond) {
		return maxLeaseDuration
	}
	return time.Duration(ms) * time.Millisecond
}

func cloneEvents(frames []OutputFrame) []OutputFrame {
	out := make([]OutputFrame, len(frames))
	for i := range frames {
		out[i] = frames[i]
		out[i].Payload = append([]byte(nil), frames[i].Payload...)
		out[i].Metadata = maps.Clone(frames[i].Metadata)
	}
	return out
}

type outputPayload struct {
	Status             string   `json:"status,omitempty"`
	CheckpointID       string   `json:"checkpoint_id,omitempty"`
	Kind               string   `json:"kind,omitempty"`
	ConsumedMessageIDs []string `json:"consumed_message_ids"`
	LLMResponseID      string   `json:"llm_response_id"`
	ToolCallID         string   `json:"tool_call_id"`
	InterruptID        string   `json:"interrupt_id"`
	OutputDelta        *string  `json:"output_delta,omitempty"`
}

type outputEventAction int

type outputMessageKeySource int

const (
	outputActionLiveOnly outputEventAction = iota
	outputActionUpdateInput
	outputActionSaveMessage
)

const (
	messageKeyFromPayloadHash outputMessageKeySource = iota
	messageKeyFromLLMResponseID
	messageKeyFromToolCallID
	messageKeyFromInterruptID
	messageKeyLatestInRun
)

type outputEventRule struct {
	action           outputEventAction
	messageType      string
	sender           string
	messageKeySource outputMessageKeySource
}

var outputEventRules = map[string]outputEventRule{
	eventpkg.EventTypeRunStatus.String():        {action: outputActionUpdateInput},
	eventpkg.EventTypeAssistantMessage.String(): {action: outputActionSaveMessage, messageType: "assistant", sender: model.SenderTypeAgent, messageKeySource: messageKeyFromLLMResponseID},
	eventpkg.EventTypeToolCall.String():         {action: outputActionSaveMessage, messageType: "tool", sender: model.SenderTypeAgent, messageKeySource: messageKeyFromToolCallID},
	eventpkg.EventTypeInputRequired.String():    {action: outputActionSaveMessage, sender: model.SenderTypeSystem, messageKeySource: messageKeyFromInterruptID},
	eventpkg.EventTypePlanUpdated.String():      {action: outputActionSaveMessage, messageType: "plan", sender: model.SenderTypeSystem, messageKeySource: messageKeyLatestInRun},
	eventpkg.EventTypeError.String():            {action: outputActionSaveMessage, messageType: "error", sender: model.SenderTypeSystem, messageKeySource: messageKeyFromPayloadHash},
	eventpkg.EventTypeAssistantDelta.String():   {action: outputActionLiveOnly},
}

func outputEventRuleFor(eventType string, payload outputPayload) outputEventRule {
	rule, ok := outputEventRules[eventType]
	if !ok {
		return outputEventRule{action: outputActionLiveOnly}
	}
	if eventType == eventpkg.EventTypeToolCall.String() && payload.OutputDelta != nil {
		rule.action = outputActionLiveOnly
	}
	if eventType == eventpkg.EventTypeInputRequired.String() {
		switch payload.Kind {
		case eventpkg.InputRequiredKindApproval:
			rule.messageType = "approval"
		case eventpkg.InputRequiredKindPlanInput:
			rule.messageType = "question"
		default:
			rule.messageType = "interrupt"
		}
	}
	return rule
}

func parseOutputPayload(raw []byte) (payload outputPayload, err error) {
	err = json.Unmarshal(raw, &payload)
	return payload, err
}

func outputMessageKey(output *OutputFrame, payload outputPayload, originalID int64, rule outputEventRule) string {
	key := ""
	switch rule.messageKeySource {
	case messageKeyFromLLMResponseID:
		key = payload.LLMResponseID
	case messageKeyFromToolCallID:
		key = payload.ToolCallID
	case messageKeyFromInterruptID:
		key = payload.InterruptID
	case messageKeyLatestInRun:
		key = "latest"
	}
	if key == "" && originalID != 0 {
		key = strconv.FormatInt(originalID, 10)
	}
	if key == "" {
		sum := sha256.Sum256(output.Payload)
		key = fmt.Sprintf("%x", sum)
	}
	return output.RunID + ":" + rule.messageType + ":" + key
}
