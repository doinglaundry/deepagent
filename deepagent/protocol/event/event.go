package event

import "encoding/json"

type Type string
type EventType = Type

func (t Type) String() string { return string(t) }

const (
	EventTypeRunStatus          Type = "run_status"
	EventTypeInputConsumed      Type = "input_consumed"
	EventTypeAssistantMessage   Type = "assistant_message"
	EventTypeToolCall           Type = "tool_call"
	EventTypeInputRequired      Type = "input_required"
	EventTypePlanUpdated        Type = "plan_updated"
	EventTypeError              Type = "error"
	EventTypeAssistantDelta     Type = "assistant_delta"
	EventTypeTokens             Type = "tokens"
	RunStatusStarted                 = "started"
	RunStatusFinished                = "finished"
	RunStatusInterrupted             = "interrupted"
	RunStatusCompactStarted          = "compact_started"
	RunStatusContextCompacted        = "context_compacted"
	RunStatusCompactInterrupted      = "compact_interrupted"
	InputRequiredKindApproval        = "approval"
	InputRequiredKindPlanInput       = "plan_input"
	InputRequiredKindFollowUp        = "follow_up"
	InputRequiredKindBatch           = "batch"
	ToolCallStatusStarted            = "started"
	ToolCallStatusFinished           = "finished"
)

type ContextUsage struct {
	UsedTokens       int64    `json:"used_tokens"`
	MaxTokens        *int64   `json:"max_tokens,omitempty"`
	Ratio            *float64 `json:"ratio,omitempty"`
	PromptTokens     *int64   `json:"prompt_tokens,omitempty"`
	CompletionTokens *int64   `json:"completion_tokens,omitempty"`
}

type TokenUsageEventPayload struct {
	PromptTokens       int64               `json:"prompt_tokens"`
	CompletionTokens   int64               `json:"completion_tokens"`
	TotalTokens        int64               `json:"total_tokens"`
	ConsumedMessageIDs []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string `json:"consumed_inputs_meta,omitempty"`
}
type MessagePartType string

const (
	MessagePartTypeText  MessagePartType = "text"
	MessagePartTypeImage MessagePartType = "image"
	MessagePartTypeAudio MessagePartType = "audio"
	MessagePartTypeVideo MessagePartType = "video"
	MessagePartTypeFile  MessagePartType = "file"
)

type MessagePart struct {
	Type       MessagePartType            `json:"type"`
	Text       string                     `json:"text,omitempty"`
	URL        string                     `json:"url,omitempty"`
	MIMEType   string                     `json:"mime_type,omitempty"`
	Base64Data string                     `json:"base64_data,omitempty"`
	Detail     string                     `json:"detail,omitempty"`
	Name       string                     `json:"name,omitempty"`
	Extra      map[string]json.RawMessage `json:"extra,omitempty"`
}
type SenderType string

const (
	SenderTypeSystem SenderType = "system"
	SenderTypeAgent  SenderType = "agent"
	SenderTypeUser   SenderType = "user"
)

type Sender struct {
	SenderType SenderType `json:"sender_type"`
	SenderID   string     `json:"sender_id"`
}
type MessageEventPayload struct {
	Status             string              `json:"status,omitempty"`
	Parts              []MessagePart       `json:"parts,omitempty"`
	MessageID          *string             `json:"message_id,omitempty"`
	Sender             *Sender             `json:"sender,omitempty"`
	LLMResponseID      string              `json:"llm_response_id,omitempty"`
	ThinkingContent    string              `json:"thinking_content,omitempty"`
	ContextUsage       *ContextUsage       `json:"context_usage,omitempty"`
	ConsumedMessageIDs []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string `json:"consumed_inputs_meta,omitempty"`
}
type AssistantDeltaEventPayload struct {
	Delta                string              `json:"delta,omitempty"`
	ThinkingContentDelta string              `json:"thinking_content_delta,omitempty"`
	LLMResponseID        string              `json:"llm_response_id,omitempty"`
	ConsumedMessageIDs   []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta   []map[string]string `json:"consumed_inputs_meta,omitempty"`
}
type ToolCallEventPayload struct {
	ToolCallID         string              `json:"tool_call_id,omitempty"`
	ToolName           string              `json:"tool_name,omitempty"`
	ArgumentsJSON      *string             `json:"arguments_json,omitempty"`
	ResultJSON         *string             `json:"result_json,omitempty"`
	OutputDelta        *string             `json:"output_delta,omitempty"`
	Status             string              `json:"status,omitempty"`
	ElapsedMs          *int64              `json:"elapsed_ms,omitempty"`
	ContextUsage       *ContextUsage       `json:"context_usage,omitempty"`
	ConsumedMessageIDs []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string `json:"consumed_inputs_meta,omitempty"`
}
type PlanItem struct {
	ID      string `json:"id"`
	Content string `json:"content"`
	Status  string `json:"status"`
}
type PlanUpdatedEventPayload struct {
	Explanation        *string             `json:"explanation,omitempty"`
	Items              []*PlanItem         `json:"items,omitempty"`
	ContextUsage       *ContextUsage       `json:"context_usage,omitempty"`
	ConsumedMessageIDs []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string `json:"consumed_inputs_meta,omitempty"`
}
type PlanInputQuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}
type PlanInputQuestion struct {
	ID       string                     `json:"id"`
	Header   string                     `json:"header,omitempty"`
	Question string                     `json:"question"`
	Options  []*PlanInputQuestionOption `json:"options,omitempty"`
}
type PlanInputRequiredEventPayload struct {
	Kind               string               `json:"kind,omitempty"`
	InterruptID        string               `json:"interrupt_id,omitempty"`
	CheckpointID       string               `json:"checkpoint_id,omitempty"`
	Questions          []*PlanInputQuestion `json:"questions,omitempty"`
	ConsumedMessageIDs []string             `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string  `json:"consumed_inputs_meta,omitempty"`
}
type ApprovalRequiredEventPayload struct {
	ToolCallID         string              `json:"tool_call_id,omitempty"`
	Kind               string              `json:"kind,omitempty"`
	InterruptID        string              `json:"interrupt_id,omitempty"`
	CheckpointID       string              `json:"checkpoint_id,omitempty"`
	ToolName           string              `json:"tool_name,omitempty"`
	ArgumentsJSON      *string             `json:"arguments_json,omitempty"`
	ConsumedMessageIDs []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string `json:"consumed_inputs_meta,omitempty"`
}
type InterruptBatchItem struct {
	Kind          string          `json:"kind"`
	InterruptID   string          `json:"interrupt_id"`
	ToolCallID    string          `json:"tool_call_id,omitempty"`
	ToolName      string          `json:"tool_name,omitempty"`
	ArgumentsJSON *string         `json:"arguments_json,omitempty"`
	InfoType      string          `json:"info_type,omitempty"`
	Info          json.RawMessage `json:"info,omitempty"`
}
type InterruptBatchRequiredEventPayload struct {
	Kind               string               `json:"kind"`
	InterruptID        string               `json:"interrupt_id"`
	CheckpointID       string               `json:"checkpoint_id"`
	Items              []InterruptBatchItem `json:"items"`
	ConsumedMessageIDs []string             `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string  `json:"consumed_inputs_meta,omitempty"`
}
type InterruptRequiredEventPayload struct {
	Kind               string              `json:"kind,omitempty"`
	InterruptID        string              `json:"interrupt_id,omitempty"`
	CheckpointID       string              `json:"checkpoint_id,omitempty"`
	InfoType           string              `json:"info_type,omitempty"`
	Info               json.RawMessage     `json:"info,omitempty"`
	ConsumedMessageIDs []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string `json:"consumed_inputs_meta,omitempty"`
}
type ErrorEventPayload struct {
	Cancelled          bool                `json:"cancelled,omitempty"`
	Status             string              `json:"status,omitempty"`
	Message            string              `json:"message,omitempty"`
	ContextUsage       *ContextUsage       `json:"context_usage,omitempty"`
	ConsumedMessageIDs []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string `json:"consumed_inputs_meta,omitempty"`
}
type CompactStartedEventPayload struct {
	Status             string              `json:"status,omitempty"`
	ContextUsage       *ContextUsage       `json:"context_usage,omitempty"`
	ConsumedMessageIDs []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string `json:"consumed_inputs_meta,omitempty"`
}
type ContextCompactedEventPayload struct {
	Status             string              `json:"status,omitempty"`
	ContextUsage       *ContextUsage       `json:"context_usage,omitempty"`
	ConsumedMessageIDs []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string `json:"consumed_inputs_meta,omitempty"`
}
type CompactInterruptedEventPayload struct {
	Status             string              `json:"status,omitempty"`
	Kind               string              `json:"kind,omitempty"`
	Reason             string              `json:"reason,omitempty"`
	ControlMessageID   string              `json:"control_message_id,omitempty"`
	CutoffMessageID    string              `json:"cutoff_message_id,omitempty"`
	ConsumedMessageIDs []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string `json:"consumed_inputs_meta,omitempty"`
}
type RunFinishedEventPayload struct {
	Status             string              `json:"status,omitempty"`
	ContextUsage       *ContextUsage       `json:"context_usage,omitempty"`
	ConsumedMessageIDs []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string `json:"consumed_inputs_meta,omitempty"`
}
