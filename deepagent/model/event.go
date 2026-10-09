package model

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cloudwego/eino/schema"
)

// RunFinishedObserver is called after a Run run-end event is converted
// to worker output. Implementations should return quickly.
type RunFinishedObserver func(ctx context.Context, ev RunEvent)

type RuntimeEvent struct {
	Sequence uint64
	Kind     string
	CallID   string
	Data     any
}

type ToolChunkSink func(context.Context, ToolCall, string) error

type ModelChunkSink func(context.Context, *Message) error

// Event payloads are shared by graph producers and the thread event stream.
type LLMTokenChunk struct {
	Message       *Message `json:"-"`
	Text          string
	ReasoningText string
	LLMResponseID string
}

type LLMEnd struct {
	Message       *Message
	LLMResponseID string
}

type LLMRequestingPayload struct{ Messages []*Message }

type ToolStartPayload struct {
	Name          string
	CallID        string
	Args          string
	ToolStartTime time.Time
}

type ToolCallOutputChunkPayload struct {
	Name   string
	CallID string
	Chunk  string
}

type ToolEndPayload struct {
	MultiContent    []schema.MessageInputPart `json:"-"`
	Name            string
	CallID          string
	ToolStartTime   time.Time
	ArgumentsInJSON string
	Result          string
	IsError         bool
}

type OutputEventType string

const (
	EventTypeRunStatus          OutputEventType = "run_status"
	EventTypeInputConsumed      OutputEventType = "input_consumed"
	EventTypeAssistantMessage   OutputEventType = "assistant_message"
	EventTypeToolCall           OutputEventType = "tool_call"
	EventTypeInputRequired      OutputEventType = "input_required"
	EventTypePlanUpdated        OutputEventType = "plan_updated"
	EventTypeError              OutputEventType = "error"
	EventTypeAssistantDelta     OutputEventType = "assistant_delta"
	EventTypeAgentActivity      OutputEventType = "agent_activity"
	EventTypeTokens             OutputEventType = "tokens"
	RunStatusStarted                            = "started"
	RunStatusFinished                           = "finished"
	RunStatusInterrupted                        = "interrupted"
	RunStatusBlocked                            = "blocked"
	RunStatusFailed                             = "failed"
	RunStatusCompactStarted                     = "compact_started"
	RunStatusContextCompacted                   = "context_compacted"
	RunStatusCompactInterrupted                 = "compact_interrupted"
	InputRequiredKindApproval                   = "approval"
	InputRequiredKindPlanInput                  = "plan_input"
	InputRequiredKindFollowUp                   = "follow_up"
	InputRequiredKindBatch                      = "batch"
	ToolCallStatusStarted                       = "started"
	ToolCallStatusFinished                      = "finished"
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

type OutputMessagePartType string

const (
	OutputMessagePartTypeText  OutputMessagePartType = "text"
	OutputMessagePartTypeImage OutputMessagePartType = "image"
	OutputMessagePartTypeAudio OutputMessagePartType = "audio"
	OutputMessagePartTypeVideo OutputMessagePartType = "video"
	OutputMessagePartTypeFile  OutputMessagePartType = "file"
)

type OutputMessagePart struct {
	Type       OutputMessagePartType      `json:"type"`
	Text       string                     `json:"text,omitempty"`
	URL        string                     `json:"url,omitempty"`
	MIMEType   string                     `json:"mime_type,omitempty"`
	Base64Data string                     `json:"base64_data,omitempty"`
	Detail     string                     `json:"detail,omitempty"`
	Name       string                     `json:"name,omitempty"`
	Extra      map[string]json.RawMessage `json:"extra,omitempty"`
}

type OutputSenderType string

const (
	OutputSenderTypeSystem OutputSenderType = "system"
	OutputSenderTypeAgent  OutputSenderType = "agent"
	OutputSenderTypeUser   OutputSenderType = "user"
)

type OutputSender struct {
	SenderType OutputSenderType `json:"sender_type"`
	SenderID   string           `json:"sender_id"`
}

type MessageEventPayload struct {
	Status             string              `json:"status,omitempty"`
	Parts              []OutputMessagePart `json:"parts,omitempty"`
	MessageID          *string             `json:"message_id,omitempty"`
	Sender             *OutputSender       `json:"sender,omitempty"`
	LLMResponseID      string              `json:"llm_response_id,omitempty"`
	ThinkingContent    string              `json:"thinking_content,omitempty"`
	ContextUsage       *ContextUsage       `json:"context_usage,omitempty"`
	ConsumedMessageIDs []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string `json:"consumed_inputs_meta,omitempty"`
}

// AgentActivityEventPayload is a live phase hint, without model request contents.
type AgentActivityEventPayload struct {
	Phase string `json:"phase"`
}

type AssistantDeltaEventPayload struct {
	Delta                string              `json:"delta,omitempty"`
	ThinkingContentDelta string              `json:"thinking_content_delta,omitempty"`
	LLMResponseID        string              `json:"llm_response_id,omitempty"`
	ConsumedMessageIDs   []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta   []map[string]string `json:"consumed_inputs_meta,omitempty"`
}

type ToolCallEventPayload struct {
	Parts              []OutputMessagePart `json:"parts,omitempty"`
	ToolCallID         string              `json:"tool_call_id,omitempty"`
	ToolName           string              `json:"tool_name,omitempty"`
	ArgumentsJSON      *string             `json:"arguments_json,omitempty"`
	ResultJSON         *string             `json:"result_json,omitempty"`
	IsError            bool                `json:"is_error,omitempty"`
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

type OutputInterruptBatchItem struct {
	Kind          string          `json:"kind"`
	InterruptID   string          `json:"interrupt_id"`
	ToolCallID    string          `json:"tool_call_id,omitempty"`
	ToolName      string          `json:"tool_name,omitempty"`
	ArgumentsJSON *string         `json:"arguments_json,omitempty"`
	InfoType      string          `json:"info_type,omitempty"`
	Info          json.RawMessage `json:"info,omitempty"`
}

type InterruptBatchRequiredEventPayload struct {
	Kind               string                     `json:"kind"`
	InterruptID        string                     `json:"interrupt_id"`
	CheckpointID       string                     `json:"checkpoint_id"`
	Items              []OutputInterruptBatchItem `json:"items"`
	ConsumedMessageIDs []string                   `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string        `json:"consumed_inputs_meta,omitempty"`
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
	CheckpointID       string              `json:"checkpoint_id,omitempty"`
	InterruptID        string              `json:"interrupt_id,omitempty"`
	Status             string              `json:"status,omitempty"`
	ContextUsage       *ContextUsage       `json:"context_usage,omitempty"`
	ConsumedMessageIDs []string            `json:"consumed_message_ids,omitempty"`
	ConsumedInputsMeta []map[string]string `json:"consumed_inputs_meta,omitempty"`
}

type RunEventType string

const (
	EventRunStart                      RunEventType = "turn_start"
	EventInputConsumed                 RunEventType = "input_consumed"
	EventLLMRequesting                 RunEventType = "llm_requesting"
	EventLLMToken                      RunEventType = "llm_token"
	EventLLMEnd                        RunEventType = "llm_end"
	EventTokens                        RunEventType = "tokens"
	EventToolStart                     RunEventType = "tool_start"
	EventToolCallOutputChunk           RunEventType = "tool_call_output_chunk"
	EventToolEnd                       RunEventType = "tool_end"
	EventApproveRequested              RunEventType = "approve_requested"
	EventFollowUpRequested             RunEventType = "followup_requested"
	EventInterrupted                   RunEventType = "interrupted"
	EventInterruptBatchRequested       RunEventType = "interrupt_batch_requested"
	EventPlanUpdated                   RunEventType = "plan_updated"
	EventContextCompactStarted         RunEventType = "context_compact_started"
	EventContextCompacted              RunEventType = "context_compacted"
	EventRunEnd                        RunEventType = "turn_end"
	EventPendingInputProcessingStarted RunEventType = "pending_input_processing_started"
	EventError                         RunEventType = "error"
	EventInterruptInfo                 RunEventType = "interrupt_info" // 将Interrupt信息带出来
)

type RunEvent struct {
	Loc            EventLocation
	ID             string
	TS             time.Time
	ThreadID       string
	RunID          string `json:"TurnID" yaml:"turnid"`
	Type           RunEventType
	Payload        any
	ConsumedInputs []*Message
	// ConsumedInputsMeta contains caller-provided metadata for ConsumedInputs.
	// When present, ConsumedInputsMeta[i] describes ConsumedInputs[i].
	ConsumedInputsMeta []any
}

type EventLocation struct {
	AgentName  string
	AgentDepth int
}

// 结构化事件载荷，避免使用 map[string]any
type RunStartPayload struct {
	Input *Message
}

type PendingInputProcessingStartedPayload struct {
	Inputs []*Message
}

type RunEndPayload struct {
	Usage        float64
	Status       string
	CheckpointID string
	InterruptID  string
}

type PlanStepStatus = string

const (
	PlanStepStatusPending    PlanStepStatus = "pending"
	PlanStepStatusInProgress PlanStepStatus = "in_progress"
	PlanStepStatusCompleted  PlanStepStatus = "completed"
)

type InterruptItemKind string

const (
	InterruptItemApprove  InterruptItemKind = "approve"
	InterruptItemFollowUp InterruptItemKind = "follow_up"
	InterruptItemCustom   InterruptItemKind = "custom"
)

type InterruptedPayload struct {
	Source       string
	InterruptID  string
	CheckpointID string
	InfoType     string
	// Info 保留业务自定义 interrupt 的原始 info。
	// 若业务希望依赖 checkpoint 在跨进程场景恢复自定义 interrupt 的 info/state，
	// 需要自行对具体类型调用 schema.Register[*YourInfo]() / schema.Register[*YourState]()。
	Info      any
	TimeoutMS int64
	Metadata  map[string]string
}

type FollowUpRequestedPayload struct {
	InterruptID  string
	CheckpointID string
	Info         *FollowUpInfo
}

type ApprovalRequiredPayload struct {
	InterruptID  string
	CheckpointID string
	ApprovalInfo *ApprovalInfo
}

type InterruptBatchItem struct {
	InterruptID string
	Kind        InterruptItemKind
	InfoType    string
	// Info 保留原始 interrupt info，供业务识别自定义中断类型。
	// 若业务希望依赖 checkpoint 在跨进程场景恢复自定义 interrupt 的 info/state，
	// 需要自行对具体类型调用 schema.Register[*YourInfo]() / schema.Register[*YourState]()。
	Info any

	ApprovalInfo *ApprovalInfo
	FollowUpInfo *FollowUpInfo
}

type InterruptBatchPayload struct {
	CheckpointID string
	Items        []InterruptBatchItem
}

type ErrorPayload struct {
	Cancelled bool
	Message   string
}

func (t OutputEventType) String() string { return string(t) }
