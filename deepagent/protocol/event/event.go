package event

type Type string

func (t Type) String() string { return string(t) }

const (
	EventTypeRunStatus         Type = "run_status"
	EventTypeAssistantMessage  Type = "assistant_message"
	EventTypeToolCall          Type = "tool_call"
	EventTypeInputRequired     Type = "input_required"
	EventTypePlanUpdated       Type = "plan_updated"
	EventTypeError             Type = "error"
	EventTypeAssistantDelta    Type = "assistant_delta"
	RunStatusFinished               = "finished"
	RunStatusInterrupted            = "interrupted"
	InputRequiredKindApproval       = "approval"
	InputRequiredKindPlanInput      = "plan_input"
)

type ToolCallEventPayload struct {
	ArgumentsJSON any    `json:"arguments_json,omitempty"`
	ToolName      string `json:"tool_name,omitempty"`
}
