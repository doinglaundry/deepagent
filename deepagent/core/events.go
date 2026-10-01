package deepagents

import (
	"errors"
	"time"

	"eino-cli/deepagent/core/internal/conversation"
	deeptools "eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	modelcomp "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// ======= 线程与轮次状态 =======

// ======= Op（输入操作） =======

// InterruptOptions describes one external request to interrupt the active Run.
//
// Metadata is intentionally opaque to  Worker/control-plane hosts
// may use it to correlate the surfaced external interrupt with their own
// protocol concepts, while the runner only passes it through to events.
type InterruptOptions struct {
	Timeout  *time.Duration
	Metadata map[string]string
}

// ======= Event（输出事件） =======

type EventType string

const (
	EventRunStart                      EventType = "turn_start"
	EventInputConsumed                 EventType = "input_consumed"
	EventLLMRequesting                 EventType = "llm_requesting"
	EventLLMToken                      EventType = "llm_token"
	EventLLMEnd                        EventType = "llm_end"
	EventTokens                        EventType = "tokens"
	EventToolStart                     EventType = "tool_start"
	EventToolCallOutputChunk           EventType = "tool_call_output_chunk"
	EventToolEnd                       EventType = "tool_end"
	EventApproveRequested              EventType = "approve_requested"
	EventFollowUpRequested             EventType = "followup_requested"
	EventInterrupted                   EventType = "interrupted"
	EventInterruptBatchRequested       EventType = "interrupt_batch_requested"
	EventPlanUpdated                   EventType = "plan_updated"
	EventContextCompactStarted         EventType = "context_compact_started"
	EventContextCompacted              EventType = "context_compacted"
	EventRunEnd                        EventType = "turn_end"
	EventPendingInputProcessingStarted EventType = "pending_input_processing_started"
	EventError                         EventType = "error"
	EventInterruptInfo                 EventType = "interrupt_info" // 将Interrupt信息带出来
)

type Event struct {
	Loc            EventLocation
	ID             string
	TS             time.Time
	ThreadID       string
	RunID          string `json:"TurnID" yaml:"turnid"`
	Type           EventType
	Payload        any
	ConsumedInputs []*schema.Message
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
	Input *schema.Message
}

type InputConsumedPayload = types.Input

type PendingInputProcessingStartedPayload struct {
	Inputs []*schema.Message
}

type LLMTokenChunk = types.LLMTokenChunk

type LLMEnd = types.LLMEnd

type TokenUsagePayload = types.Usage

type ToolStartPayload = types.ToolStartPayload

type ToolCallOutputChunkPayload = types.ToolCallOutputChunkPayload

type ToolEndPayload = types.ToolEndPayload

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

type PlanStep = types.PlanStep

type PlanUpdatedPayload = deeptools.PlanUpdate

type ContextCompactedPayload = conversation.ContextCompactedPayload

type ContextCompactStartedPayload = conversation.ContextCompactStartedPayload

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
	Info         *deeptools.FollowUpInfo
}

type ApprovalRequiredPayload struct {
	InterruptID  string
	CheckpointID string
	ApprovalInfo *deeptools.ApprovalInfo
}

type InterruptBatchItem struct {
	InterruptID string
	Kind        InterruptItemKind
	InfoType    string
	// Info 保留原始 interrupt info，供业务识别自定义中断类型。
	// 若业务希望依赖 checkpoint 在跨进程场景恢复自定义 interrupt 的 info/state，
	// 需要自行对具体类型调用 schema.Register[*YourInfo]() / schema.Register[*YourState]()。
	Info any

	ApprovalInfo *deeptools.ApprovalInfo
	FollowUpInfo *deeptools.FollowUpInfo
}

type InterruptBatchPayload struct {
	CheckpointID string
	Items        []InterruptBatchItem
}

type ErrorPayload struct {
	Cancelled bool
	Message   string
}

// ======= 错误定义 =======

var (
	ErrThreadBackpressure = errors.New("agentthread: input queue is full (backpressure)")
	ErrInvalidOp          = errors.New("agentthread: invalid op")
	ErrThreadRunning      = errors.New("agentthread: thread already has an active Run")
	ErrNoActiveRun        = errors.New("agentthread: no active Run")
	ErrRunInputClosed     = errors.New("agentthread: current Run input is closed")
)

// ======= 复用外部 Message 类型 =======

// Message 直接复用 Eino 的 schema.Message 类型，避免重复定义。
type Message = schema.Message

// LLMRequestingPayload 直接复用 model.CallbackInput，避免复制一层字段。
type LLMRequestingPayload = modelcomp.CallbackInput
