package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	agentmodel "eino-cli/deepagent/model"
)

func createThread(req agentmodel.SubmitRequest, id int64) *agentmodel.ThreadRecord {
	metadata := maps.Clone(req.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	title := strings.TrimSpace(req.Title)
	if title != "" {
		metadata["title"] = title
	}
	var profile *agentmodel.ThreadProfile
	if req.Profile != nil && *req.Profile != (agentmodel.ThreadProfile{}) {
		copy := *req.Profile
		profile = &copy
	}
	return &agentmodel.ThreadRecord{ThreadID: id, UserID: req.UserID, SessionID: req.SessionID, Status: agentmodel.ThreadStatusOpen, Metadata: metadata, Profile: profile}
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

func cloneEvents(frames []agentmodel.OutputFrame) []agentmodel.OutputFrame {
	out := make([]agentmodel.OutputFrame, len(frames))
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
	agentmodel.EventTypeRunStatus.String():        {action: outputActionUpdateInput},
	agentmodel.EventTypeAssistantMessage.String(): {action: outputActionSaveMessage, messageType: "assistant", sender: agentmodel.MailboxSenderTypeAgent, messageKeySource: messageKeyFromLLMResponseID},
	agentmodel.EventTypeToolCall.String():         {action: outputActionSaveMessage, messageType: "tool", sender: agentmodel.MailboxSenderTypeAgent, messageKeySource: messageKeyFromToolCallID},
	agentmodel.EventTypeInputRequired.String():    {action: outputActionSaveMessage, sender: agentmodel.MailboxSenderTypeSystem, messageKeySource: messageKeyFromInterruptID},
	agentmodel.EventTypePlanUpdated.String():      {action: outputActionSaveMessage, messageType: "plan", sender: agentmodel.MailboxSenderTypeSystem, messageKeySource: messageKeyLatestInRun},
	agentmodel.EventTypeError.String():            {action: outputActionSaveMessage, messageType: "error", sender: agentmodel.MailboxSenderTypeSystem, messageKeySource: messageKeyFromPayloadHash},
	agentmodel.EventTypeAssistantDelta.String():   {action: outputActionLiveOnly},
	agentmodel.EventTypeAgentActivity.String():    {action: outputActionLiveOnly},
}

func outputEventRuleFor(eventType string, payload outputPayload) outputEventRule {
	rule, ok := outputEventRules[eventType]
	if !ok {
		return outputEventRule{action: outputActionLiveOnly}
	}
	if eventType == agentmodel.EventTypeToolCall.String() && payload.OutputDelta != nil {
		rule.action = outputActionLiveOnly
	}
	if eventType == agentmodel.EventTypeInputRequired.String() {
		switch payload.Kind {
		case agentmodel.InputRequiredKindApproval:
			rule.messageType = "approval"
		case agentmodel.InputRequiredKindPlanInput:
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

func outputMessageKey(output *agentmodel.OutputFrame, payload outputPayload, originalID int64, rule outputEventRule) string {
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

func memoryKey(kind, key string) string {
	sum := sha256.Sum256([]byte(key))
	return "deepagent:memory:" + kind + ":" + hex.EncodeToString(sum[:])
}

func validMemoryRequest(key string, ttl time.Duration) error {
	if strings.TrimSpace(key) == "" {
		return errors.New("memory key is required")
	}
	if ttl <= 0 {
		return errors.New("memory lease duration must be positive")
	}
	return nil
}

func sessionEventKey(sessionID string) string { return "deepagent:session:" + sessionID + ":events" }

func sessionChannel(sessionID string) string { return "deepagent:session:" + sessionID + ":live" }

func newSubscription(ctx context.Context, stream *StreamStreamOut, req agentmodel.SubscribeSessionRequest, maxIdle time.Duration) *agentmodel.Subscription {
	events := make(chan agentmodel.OutputFrame, 32)
	subCtx, cancel := context.WithCancel(ctx)
	raw, closePubSub, err := stream.redis.Subscribe(subCtx, sessionChannel(req.SessionID))
	if err != nil {
		cancel()
		close(events)
		return &agentmodel.Subscription{Events: events, Err: err, Close: func() error { return nil }}
	}
	closed := make(chan struct{})
	go func() {
		defer close(events)
		defer close(closed)
		defer closePubSub()
		defer cancel()
		last, _ := strconv.ParseInt(req.RecoverQueueID, 10, 64)
		send := func(data []byte) bool {
			var item streamEnvelope
			if json.Unmarshal(data, &item) != nil || item.Seq <= last {
				return true
			}
			select {
			case events <- item.Frame:
				last = item.Seq
				return true
			case <-subCtx.Done():
				return false
			}
		}
		members, err := stream.redis.ZRange(subCtx, sessionEventKey(req.SessionID), 0, -1)
		if err == nil {
			for _, member := range members {
				seq, parseErr := strconv.ParseInt(member, 10, 64)
				if parseErr != nil || seq <= last {
					continue
				}
				data, getErr := stream.redis.GetRaw(subCtx, fmt.Sprintf("%s:%d", sessionEventKey(req.SessionID), seq))
				if getErr == nil && !send(data) {
					return
				}
			}
		}
		var idle <-chan time.Time
		var timer *time.Timer
		if maxIdle > 0 {
			timer = time.NewTimer(maxIdle)
			defer timer.Stop()
			idle = timer.C
		}
		for {
			select {
			case data, ok := <-raw:
				if !ok {
					return
				}
				if !send(data) {
					return
				}
				if timer != nil {
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					timer.Reset(maxIdle)
				}
			case <-idle:
				return
			case <-subCtx.Done():
				return
			}
		}
	}()
	return &agentmodel.Subscription{Events: events, Close: func() error { cancel(); <-closed; return nil }}
}
