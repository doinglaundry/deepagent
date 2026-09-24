package thread

import (
	"encoding/json"
	"testing"

	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	eventpkg "eino-cli/deepagent/protocol/event"
	corethread "eino-cli/deepagent/thread"
)

func outputItem(t *testing.T, kind eventpkg.Type, payload any) corethread.TransportThreadOutputItem {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return corethread.TransportThreadOutputItem{Event: &corethread.TransportEvent{ID: "event", ThreadID: "thread", RunID: "run", Type: corethread.TransportEventType(kind), Payload: raw}}
}

func TestManagedInputConsumedIdentifiesOnlyCurrentMessage(t *testing.T) {
	b := managedOutput{owner: api.Thread{ID: "thread"}}
	id := "two"
	item := outputItem(t, eventpkg.EventTypeInputConsumed, eventpkg.MessageEventPayload{MessageID: &id, Parts: []eventpkg.MessagePart{{Type: "text", Text: "describe"}, {Type: "image", URL: "image-url"}}, ConsumedMessageIDs: []string{"one", "two"}})
	events, err := b.convert(item)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%v err=%v", events, err)
	}
	e := events[0]
	if e.Kind != protocol.EventInputConsumed || e.Text != "describe" || len(e.MessageIDs) != 1 || e.MessageIDs[0] != "two" || string(e.Data) != string(item.Event.Payload) {
		t.Fatalf("consumption event=%+v", e)
	}
	if len(b.consumed) != 2 {
		t.Fatal("single-message event erased run ownership")
	}
}
func TestManagedOutputDefersTerminalUntilFinalYield(t *testing.T) {
	bridge := managedOutput{owner: api.Thread{ID: "thread", SessionID: "session"}}
	started := outputItem(t, eventpkg.EventTypeRunStatus, eventpkg.MessageEventPayload{Status: "started", ConsumedMessageIDs: []string{"input"}})
	events, err := bridge.convert(started)
	if err != nil || len(events) != 1 || events[0].Kind != protocol.EventRunStarted {
		t.Fatalf("events=%v err=%v", events, err)
	}
	finished := outputItem(t, eventpkg.EventTypeRunStatus, eventpkg.RunFinishedEventPayload{Status: "finished", ConsumedMessageIDs: []string{"input", "appended"}})
	events, err = bridge.convert(finished)
	if err != nil || len(events) != 0 {
		t.Fatalf("premature completion: %v %v", events, err)
	}
	events, err = bridge.convert(corethread.TransportThreadOutputItem{Yield: &corethread.TransportThreadYield{Reason: "finished"}})
	if err != nil || len(events) != 1 || events[0].Kind != protocol.EventRunCompleted || len(events[0].MessageIDs) != 2 {
		t.Fatalf("terminal=%v err=%v", events, err)
	}
	events, err = bridge.convert(corethread.TransportThreadOutputItem{Yield: &corethread.TransportThreadYield{Reason: "finished"}})
	if err != nil || len(events) != 0 {
		t.Fatal("duplicate terminal")
	}
}
func TestManagedOutputApprovalWaitsForRunEnd(t *testing.T) {
	bridge := managedOutput{owner: api.Thread{ID: "thread"}}
	args := `{"path":"file"}`
	requested := outputItem(t, eventpkg.EventTypeInputRequired, eventpkg.ApprovalRequiredEventPayload{Kind: "approval", ToolName: "write_file", ArgumentsJSON: &args, InterruptID: "interrupt", CheckpointID: "checkpoint"})
	requested.Yield = &corethread.TransportThreadYield{Reason: "blocked", Block: &corethread.TransportPendingBlock{RunID: "run", InterruptID: "interrupt", CheckpointID: "checkpoint"}}
	events, err := bridge.convert(requested)
	if err != nil || len(events) != 0 {
		t.Fatalf("early blocked terminal: %v %v", events, err)
	}
	end := outputItem(t, eventpkg.EventTypeRunStatus, eventpkg.RunFinishedEventPayload{Status: "finished", ConsumedMessageIDs: []string{"input"}})
	end.Yield = &corethread.TransportThreadYield{Reason: "finished"}
	events, err = bridge.convert(end)
	if err != nil || len(events) != 1 || events[0].Kind != protocol.EventBlocked || events[0].Block.Arguments != args || events[0].Block.InterruptID != "interrupt" {
		t.Fatalf("block=%v err=%v", events, err)
	}
}

func TestManagedOutputFollowUpPreservesOptions(t *testing.T) {
	for _, tc := range []struct {
		info     string
		question string
		options  int
	}{
		{`{"question":"Which format?","questions":["JSON","YAML"]}`, "Which format?", 2},
		{`{"questions":["First question?","Second question?"]}`, "First question?\nSecond question?", 0},
	} {
		bridge := managedOutput{owner: api.Thread{ID: "thread"}}
		item := outputItem(t, eventpkg.EventTypeInputRequired, eventpkg.InterruptRequiredEventPayload{Kind: eventpkg.InputRequiredKindFollowUp, InterruptID: "interrupt", CheckpointID: "checkpoint", Info: json.RawMessage(tc.info)})
		if _, err := bridge.convert(item); err != nil {
			t.Fatal(err)
		}
		end := outputItem(t, eventpkg.EventTypeRunStatus, eventpkg.RunFinishedEventPayload{Status: "finished"})
		end.Yield = &corethread.TransportThreadYield{Reason: "finished"}
		events, err := bridge.convert(end)
		if err != nil || len(events) != 1 {
			t.Fatalf("events=%v err=%v", events, err)
		}
		block := events[0].Block
		if block == nil || block.Kind != "clarification" || block.Question != tc.question || len(block.Options) != tc.options {
			t.Fatalf("clarification lost content: %+v", block)
		}
		if tc.options > 0 && block.Options[1] != "YAML" {
			t.Fatal("options reordered")
		}
	}
}
func TestManagedOutputPreservesMediaToolChunksAndFailure(t *testing.T) {
	bridge := managedOutput{owner: api.Thread{ID: "thread"}}
	item := outputItem(t, eventpkg.EventTypeAssistantMessage, eventpkg.MessageEventPayload{LLMResponseID: "response", Parts: []eventpkg.MessagePart{{Type: "text", Text: "answer"}, {Type: "image", URL: "image-url"}}})
	events, err := bridge.convert(item)
	if err != nil || len(events) != 1 || events[0].Text != "answer" || events[0].ResponseID != "response" || string(events[0].Data) != string(item.Event.Payload) {
		t.Fatalf("message=%v err=%v", events, err)
	}
	chunk := "chunk"
	events, err = bridge.convert(outputItem(t, eventpkg.EventTypeToolCall, eventpkg.ToolCallEventPayload{Status: "started", ToolCallID: "call", ToolName: "shell", OutputDelta: &chunk}))
	if err != nil || len(events) != 1 || events[0].Kind != protocol.EventToolOutput || events[0].Text != chunk {
		t.Fatalf("chunk=%v err=%v", events, err)
	}
	events, err = bridge.convert(outputItem(t, eventpkg.EventTypeError, eventpkg.ErrorEventPayload{Message: "model failed"}))
	if err != nil || len(events) != 0 {
		t.Fatal("error released run before final yield")
	}
	events, err = bridge.convert(corethread.TransportThreadOutputItem{Yield: &corethread.TransportThreadYield{Reason: "finished"}})
	if err != nil || len(events) != 1 || events[0].Kind != protocol.EventRunFailed || events[0].Error != "model failed" {
		t.Fatalf("failure=%v err=%v", events, err)
	}
}
