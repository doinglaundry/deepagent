package event

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestEventPayloadJSONContract(t *testing.T) {
	args, result, delta := `{"query":"go"}`, "done", "partial"
	cases := []struct {
		name    string
		payload any
		want    map[string]any
	}{
		{"tool call", ToolCallEventPayload{
			ToolCallID: "call-1", ToolName: "search", ArgumentsJSON: &args,
			ResultJSON: &result, OutputDelta: &delta, Status: ToolCallStatusFinished,
		}, map[string]any{
			"tool_call_id": "call-1", "tool_name": "search", "arguments_json": args,
			"result_json": result, "output_delta": delta, "status": ToolCallStatusFinished,
		}},
		{"optional tool fields", ToolCallEventPayload{ToolCallID: "call-1"}, map[string]any{"tool_call_id": "call-1"}},
		{"mandatory plan fields", PlanItem{}, map[string]any{"id": "", "content": "", "status": ""}},
		{"compact interruption", CompactInterruptedEventPayload{
			Status: RunStatusCompactInterrupted, Kind: "cancel_input", Reason: "user requested",
			ControlMessageID: "control-1", CutoffMessageID: "message-1",
		}, map[string]any{
			"status": RunStatusCompactInterrupted, "kind": "cancel_input", "reason": "user requested",
			"control_message_id": "control-1", "cutoff_message_id": "message-1",
		}},
		{"optional compact fields", CompactInterruptedEventPayload{}, map[string]any{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			err = json.Unmarshal(raw, &got)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("payload = %s, want %v", raw, tc.want)
			}
		})
	}
}
