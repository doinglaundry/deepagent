package thread

import (
	"encoding/json"
	"fmt"
	"strings"

	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	eventpkg "eino-cli/deepagent/protocol/event"
	corethread "eino-cli/deepagent/thread"
)

// managedOutput owns only one bridge's pending terminal status. Thread emits
// interrupt notifications before its final run-end Yield; defer termination
// until that final boundary so the Worker cannot release ownership early.
type managedOutput struct {
	owner       api.Thread
	runID       string
	consumed    []string
	block       *protocol.Block
	failure     string
	interrupted bool
	finished    bool
}

func (b *managedOutput) convert(item corethread.TransportThreadOutputItem) ([]protocol.Event, error) {
	var out []protocol.Event
	if item.Event != nil {
		events, err := b.event(item.Event)
		if err != nil {
			return nil, err
		}
		out = append(out, events...)
	}
	if item.Yield == nil {
		return out, nil
	}
	y := item.Yield
	if y.Err != nil {
		b.failure = y.Err.Error()
	}
	switch y.Reason {
	case "blocked":
		if y.Block == nil || b.block == nil || y.Block.RunID != b.block.RunID || y.Block.CheckpointID != b.block.CheckpointID || y.Block.InterruptID != b.block.InterruptID {
			return nil, fmt.Errorf("blocked yield does not match input-required event")
		}
	case "interrupted":
		b.interrupted = true
	case "finished":
		if b.finished {
			return out, nil
		}
		if b.runID == "" {
			return nil, fmt.Errorf("run ended without identity")
		}
		final := protocol.Event{ID: protocol.NewID("event"), Namespace: b.owner.Namespace, ThreadID: b.owner.ID, SessionID: b.owner.SessionID, RunID: b.runID, MessageIDs: append([]string(nil), b.consumed...), Kind: protocol.EventRunCompleted}
		switch {
		case b.failure != "":
			final.Kind = protocol.EventRunFailed
			final.Error = b.failure
		case b.block != nil:
			final.Kind = protocol.EventBlocked
			final.Block = b.block
		case b.interrupted:
			final.Kind = protocol.EventRunCancelled
		}
		b.finished = true
		out = append(out, final)
	default:
		return nil, fmt.Errorf("unsupported thread yield %q", y.Reason)
	}
	return out, nil
}
func (b *managedOutput) event(in *corethread.TransportEvent) ([]protocol.Event, error) {
	if in.ThreadID != "" && in.ThreadID != b.owner.ID {
		return nil, fmt.Errorf("output belongs to another thread")
	}
	var common eventpkg.MessageEventPayload
	if err := json.Unmarshal(in.Payload, &common); err != nil {
		return nil, err
	}
	if b.runID != "" && in.RunID != b.runID && !b.finished {
		return nil, fmt.Errorf("interleaved run output")
	}
	newRun := in.RunID != b.runID
	if newRun || (eventpkg.Type(in.Type) == eventpkg.EventTypeRunStatus && common.Status == eventpkg.RunStatusStarted) {
		b.runID = in.RunID
		b.block = nil
		b.failure = ""
		b.interrupted = false
		b.finished = false
	}
	b.consumed = append([]string(nil), common.ConsumedMessageIDs...)
	event := protocol.Event{ID: in.ID, Namespace: b.owner.Namespace, SessionID: b.owner.SessionID, ThreadID: b.owner.ID, RunID: in.RunID, MessageIDs: append([]string(nil), b.consumed...), CreatedAt: in.TS, Data: append(json.RawMessage(nil), in.Payload...)}
	switch eventpkg.Type(in.Type) {
	case eventpkg.EventTypeRunStatus:
		switch common.Status {
		case eventpkg.RunStatusStarted:
			event.Kind = protocol.EventRunStarted
		case eventpkg.RunStatusFinished:
			return nil, nil
		case eventpkg.RunStatusInterrupted:
			b.interrupted = true
			return nil, nil
		case eventpkg.RunStatusCompactStarted:
			if !newRun {
				return nil, nil
			}
			event.Kind = protocol.EventRunStarted
		case eventpkg.RunStatusContextCompacted:
			event.Kind = protocol.EventCompacted
		case eventpkg.RunStatusCompactInterrupted:
			b.interrupted = true
			return nil, nil
		default:
			return nil, fmt.Errorf("unknown run status %q", common.Status)
		}
	case eventpkg.EventTypeAssistantDelta:
		var payload eventpkg.AssistantDeltaEventPayload
		if err := json.Unmarshal(in.Payload, &payload); err != nil {
			return nil, err
		}
		event.Kind = protocol.EventTextDelta
		event.Text = payload.Delta
		event.ResponseID = payload.LLMResponseID
	case eventpkg.EventTypeAssistantMessage:
		event.Kind = protocol.EventText
		event.ResponseID = common.LLMResponseID
		for _, part := range common.Parts {
			if part.Type == eventpkg.MessagePartTypeText {
				event.Text += part.Text
			}
		}
	case eventpkg.EventTypeInputConsumed:
		event.Kind = protocol.EventInputConsumed
		event.MessageIDs = nil
		if common.MessageID != nil {
			event.MessageIDs = []string{*common.MessageID}
		}
		for _, part := range common.Parts {
			if part.Type == eventpkg.MessagePartTypeText {
				event.Text += part.Text
			}
		}
	case eventpkg.EventTypeToolCall:
		var payload eventpkg.ToolCallEventPayload
		if err := json.Unmarshal(in.Payload, &payload); err != nil {
			return nil, err
		}
		event.ToolCallID = payload.ToolCallID
		event.ToolName = payload.ToolName
		if payload.ArgumentsJSON != nil {
			event.Arguments = *payload.ArgumentsJSON
		}
		switch {
		case payload.OutputDelta != nil:
			event.Kind = protocol.EventToolOutput
			event.Text = *payload.OutputDelta
		case payload.Status == eventpkg.ToolCallStatusStarted:
			event.Kind = protocol.EventToolStarted
		case payload.Status == eventpkg.ToolCallStatusFinished:
			event.Kind = protocol.EventToolCompleted
			if payload.ResultJSON != nil {
				event.Text = *payload.ResultJSON
			}
		default:
			return nil, fmt.Errorf("unknown tool status %q", payload.Status)
		}
	case eventpkg.EventTypeTokens:
		var payload eventpkg.TokenUsageEventPayload
		if err := json.Unmarshal(in.Payload, &payload); err != nil {
			return nil, err
		}
		event.Kind = protocol.EventTokens
		event.Text = fmt.Sprint(payload.TotalTokens)
	case eventpkg.EventTypePlanUpdated:
		event.Kind = protocol.EventPlan
	case eventpkg.EventTypeError:
		var payload eventpkg.ErrorEventPayload
		if err := json.Unmarshal(in.Payload, &payload); err != nil {
			return nil, err
		}
		if payload.Cancelled {
			b.interrupted = true
			return nil, nil
		}
		b.failure = payload.Message
		if b.failure == "" {
			b.failure = "agent execution failed"
		}
		return nil, nil
	case eventpkg.EventTypeInputRequired:
		var payload eventpkg.ApprovalRequiredEventPayload
		if err := json.Unmarshal(in.Payload, &payload); err != nil {
			return nil, err
		}
		block := &protocol.Block{RunID: in.RunID, CheckpointID: payload.CheckpointID, InterruptID: payload.InterruptID, Kind: payload.Kind, ToolName: payload.ToolName}
		switch payload.Kind {
		case eventpkg.InputRequiredKindApproval:
			block.Question = "Allow " + payload.ToolName + "?"
			if payload.ArgumentsJSON != nil {
				block.Arguments = *payload.ArgumentsJSON
			}
		case eventpkg.InputRequiredKindFollowUp:
			var follow eventpkg.InterruptRequiredEventPayload
			if err := json.Unmarshal(in.Payload, &follow); err != nil {
				return nil, err
			}
			var info struct {
				Question  string
				Questions []string
			}
			if err := json.Unmarshal(follow.Info, &info); err != nil {
				return nil, err
			}
			block.Kind = "clarification"
			block.Question = info.Question
			if block.Question == "" {
				block.Question = strings.Join(info.Questions, "\n")
			} else {
				// ask_user stores its choices in the retained Questions field.
				// Legacy multi-question prompts have no leading Question.
				block.Options = append([]string(nil), info.Questions...)
			}
		default:
			return nil, fmt.Errorf("managed protocol cannot represent interrupt kind %q", payload.Kind)
		}
		if block.CheckpointID == "" || block.InterruptID == "" {
			return nil, fmt.Errorf("input-required event missing correlation")
		}
		if b.block != nil && b.block.InterruptID != block.InterruptID {
			return nil, fmt.Errorf("managed protocol cannot represent simultaneous interrupts")
		}
		b.block = block
		return nil, nil
	default:
		return nil, fmt.Errorf("unsupported thread event %q", in.Type)
	}
	return []protocol.Event{event}, nil
}
