package run

import (
	"context"
	"fmt"
	"maps"
	"time"

	deeptools "eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
)

// InterruptOptions describes one external request to interrupt the active Run.
//
// Metadata is intentionally opaque to  Worker/control-plane hosts
// may use it to correlate the surfaced external interrupt with their own
// protocol concepts, while the runner only passes it through to events.
type InterruptOptions struct {
	Timeout  *time.Duration
	Metadata map[string]string
}

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
	ConsumedInputs []*messagepkg.Message
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
	Input *messagepkg.Message
}

type PendingInputProcessingStartedPayload struct {
	Inputs []*messagepkg.Message
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

func (r *Run) PublishEvent(ctx context.Context, kind EventType, payload any) error {
	r.mu.Lock()
	messages := make([]*messagepkg.Message, 0, len(r.consumed))
	metadata := make([]any, 0, len(r.consumed))
	for _, input := range r.consumed {
		messages = append(messages, types.CopyMessage(input.Message))
		metadata = append(metadata, input.Meta)
	}
	r.mu.Unlock()
	id := uuid.NewString()
	if r.config.EventIDProvider != nil {
		id = r.config.EventIDProvider(ctx, r.config.Graph.ThreadID, r.id)
	}
	event := Event{ID: id, TS: time.Now(), ThreadID: r.config.Graph.ThreadID, RunID: r.id, Type: kind, Payload: payload, ConsumedInputs: messages, ConsumedInputsMeta: metadata, Loc: EventLocation{AgentName: r.config.Graph.Name, AgentDepth: r.config.Graph.Depth}}
	select {
	case r.config.Events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Run) EmitBlocked(ctx context.Context, info *compose.InterruptInfo) error {
	checkpointID := r.CheckpointID()
	r.mu.Lock()
	request := r.interruptOpts
	r.mu.Unlock()
	if request != nil {
		payload := InterruptedPayload{Source: "external", CheckpointID: checkpointID, Metadata: maps.Clone(request.Metadata)}
		if request.Timeout != nil {
			payload.TimeoutMS = request.Timeout.Milliseconds()
		}
		err := r.PublishEvent(ctx, EventInterrupted, payload)
		if err != nil {
			return err
		}
		return r.PublishEvent(ctx, EventInterruptInfo, info)
	}
	if len(info.InterruptContexts) > 1 {
		items := make([]InterruptBatchItem, 0, len(info.InterruptContexts))
		for _, interrupt := range info.InterruptContexts {
			items = append(items, interruptBatchItem(interrupt))
		}
		err := r.PublishEvent(ctx, EventInterruptBatchRequested, InterruptBatchPayload{CheckpointID: checkpointID, Items: items})
		if err != nil {
			return err
		}
		return r.PublishEvent(ctx, EventInterruptInfo, info)
	}
	if len(info.InterruptContexts) == 0 {
		err := r.PublishEvent(ctx, EventInterrupted, InterruptedPayload{Source: "external", CheckpointID: checkpointID})
		if err != nil {
			return err
		}
	}
	for _, interrupt := range info.InterruptContexts {
		kind := EventInterrupted
		var payload any = InterruptedPayload{Source: "custom", CheckpointID: checkpointID}
		if interrupt != nil {
			switch data := interrupt.Info.(type) {
			case *deeptools.ApprovalInfo:
				kind = EventApproveRequested
				payload = ApprovalRequiredPayload{InterruptID: interrupt.ID, CheckpointID: checkpointID, ApprovalInfo: data}
			case *deeptools.FollowUpInfo:
				kind = EventFollowUpRequested
				payload = FollowUpRequestedPayload{InterruptID: interrupt.ID, CheckpointID: checkpointID, Info: data}
			default:
				payload = InterruptedPayload{Source: "custom", InterruptID: interrupt.ID, CheckpointID: checkpointID, InfoType: fmt.Sprintf("%T", data), Info: data}
			}
		}
		err := r.PublishEvent(ctx, kind, payload)
		if err != nil {
			return err
		}
	}
	return r.PublishEvent(ctx, EventInterruptInfo, info)
}

func interruptBatchItem(interrupt *compose.InterruptCtx) InterruptBatchItem {
	if interrupt == nil {
		return InterruptBatchItem{Kind: InterruptItemCustom, InfoType: "<nil>"}
	}
	item := InterruptBatchItem{InterruptID: interrupt.ID, InfoType: fmt.Sprintf("%T", interrupt.Info), Info: interrupt.Info}
	switch data := interrupt.Info.(type) {
	case *deeptools.ApprovalInfo:
		item.Kind, item.ApprovalInfo = InterruptItemApprove, data
	case *deeptools.FollowUpInfo:
		item.Kind, item.FollowUpInfo = InterruptItemFollowUp, data
	default:
		item.Kind = InterruptItemCustom
	}
	return item
}
