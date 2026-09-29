package manager

import (
	"encoding/json"
	"testing"

	eventpkg "eino-cli/deepagent/protocol/event"
)

func TestToolCallOutputRoutingFromPublicPayload(t *testing.T) {
	delta, result := "partial result", "completed result"
	cases := []struct {
		name    string
		payload eventpkg.ToolCallEventPayload
		action  outputEventAction
	}{
		{"streaming", eventpkg.ToolCallEventPayload{ToolCallID: "call-1", OutputDelta: &delta}, outputActionLiveOnly},
		{"completed", eventpkg.ToolCallEventPayload{ToolCallID: "call-1", ResultJSON: &result, Status: eventpkg.ToolCallStatusFinished}, outputActionSaveMessage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := parseOutputPayload(raw)
			if err != nil {
				t.Fatal(err)
			}
			rule := outputEventRuleFor(eventpkg.EventTypeToolCall.String(), payload)
			if rule.action != tc.action || rule.messageType != "tool" {
				t.Fatalf("rule = %+v, want action %d and tool message type", rule, tc.action)
			}
			if tc.payload.OutputDelta != nil && (payload.OutputDelta == nil || *payload.OutputDelta != delta) {
				t.Fatalf("output delta lost in %s", raw)
			}
		})
	}
}
