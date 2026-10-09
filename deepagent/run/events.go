package run

import (
	"context"
	"fmt"
	"maps"
	"time"

	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/compose"
	"github.com/google/uuid"
)

func (r *Run) PublishEvent(ctx context.Context, kind agentmodel.RunEventType, payload any) error {
	r.mu.Lock()
	messages := make([]*agentmodel.Message, 0, len(r.consumed))
	metadata := make([]any, 0, len(r.consumed))
	for _, input := range r.consumed {
		messages = append(messages, agentmodel.CopyMessage(input.Message))
		metadata = append(metadata, input.Meta)
	}
	r.mu.Unlock()
	id := uuid.NewString()
	if r.config.EventIDProvider != nil {
		id = r.config.EventIDProvider(ctx, r.config.Graph.ThreadID, r.id)
	}
	event := agentmodel.RunEvent{ID: id, TS: time.Now(), ThreadID: r.config.Graph.ThreadID, RunID: r.id, Type: kind, Payload: payload, ConsumedInputs: messages, ConsumedInputsMeta: metadata, Loc: agentmodel.EventLocation{AgentName: r.config.Graph.Name, AgentDepth: r.config.Graph.Depth}}
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
		payload := agentmodel.InterruptedPayload{Source: "external", CheckpointID: checkpointID, Metadata: maps.Clone(request.Metadata)}
		if request.Timeout != nil {
			payload.TimeoutMS = request.Timeout.Milliseconds()
		}
		err := r.PublishEvent(ctx, agentmodel.EventInterrupted, payload)
		if err != nil {
			return err
		}
		return r.PublishEvent(ctx, agentmodel.EventInterruptInfo, info)
	}
	if len(info.InterruptContexts) > 1 {
		items := make([]agentmodel.InterruptBatchItem, 0, len(info.InterruptContexts))
		for _, interrupt := range info.InterruptContexts {
			items = append(items, interruptBatchItem(interrupt))
		}
		err := r.PublishEvent(ctx, agentmodel.EventInterruptBatchRequested, agentmodel.InterruptBatchPayload{CheckpointID: checkpointID, Items: items})
		if err != nil {
			return err
		}
		return r.PublishEvent(ctx, agentmodel.EventInterruptInfo, info)
	}
	if len(info.InterruptContexts) == 0 {
		err := r.PublishEvent(ctx, agentmodel.EventInterrupted, agentmodel.InterruptedPayload{Source: "external", CheckpointID: checkpointID})
		if err != nil {
			return err
		}
	}
	for _, interrupt := range info.InterruptContexts {
		kind := agentmodel.EventInterrupted
		var payload any = agentmodel.InterruptedPayload{Source: "custom", CheckpointID: checkpointID}
		if interrupt != nil {
			switch data := interrupt.Info.(type) {
			case *agentmodel.ApprovalInfo:
				kind = agentmodel.EventApproveRequested
				payload = agentmodel.ApprovalRequiredPayload{InterruptID: interrupt.ID, CheckpointID: checkpointID, ApprovalInfo: data}
			case *agentmodel.FollowUpInfo:
				kind = agentmodel.EventFollowUpRequested
				payload = agentmodel.FollowUpRequestedPayload{InterruptID: interrupt.ID, CheckpointID: checkpointID, Info: data}
			default:
				payload = agentmodel.InterruptedPayload{Source: "custom", InterruptID: interrupt.ID, CheckpointID: checkpointID, InfoType: fmt.Sprintf("%T", data), Info: data}
			}
		}
		err := r.PublishEvent(ctx, kind, payload)
		if err != nil {
			return err
		}
	}
	return r.PublishEvent(ctx, agentmodel.EventInterruptInfo, info)
}

func interruptBatchItem(interrupt *compose.InterruptCtx) agentmodel.InterruptBatchItem {
	if interrupt == nil {
		return agentmodel.InterruptBatchItem{Kind: agentmodel.InterruptItemCustom, InfoType: "<nil>"}
	}
	item := agentmodel.InterruptBatchItem{InterruptID: interrupt.ID, InfoType: fmt.Sprintf("%T", interrupt.Info), Info: interrupt.Info}
	switch data := interrupt.Info.(type) {
	case *agentmodel.ApprovalInfo:
		item.Kind, item.ApprovalInfo = agentmodel.InterruptItemApprove, data
	case *agentmodel.FollowUpInfo:
		item.Kind, item.FollowUpInfo = agentmodel.InterruptItemFollowUp, data
	default:
		item.Kind = agentmodel.InterruptItemCustom
	}
	return item
}
