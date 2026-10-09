package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"eino-cli/deepagent/dal/cache"
	"eino-cli/deepagent/dal/db"
	agentmodel "eino-cli/deepagent/model"
)

func (c *Manager) Submit(ctx context.Context, req agentmodel.SubmitRequest) (result agentmodel.ThreadMessageResult, err error) {
	if req.ThreadID != 0 && req.Input == nil {
		return result, errors.New("input is required")
	}
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		var thread *agentmodel.ThreadRecord
		if req.ThreadID != 0 {
			var err error
			thread, err = c.lockThread(txCtx, req.ThreadID)
			if err != nil {
				return err
			}
			if thread.Status != agentmodel.ThreadStatusOpen {
				return ErrThreadClosed
			}
		} else {
			id, err := cache.GenerateID(txCtx, c.redis)
			if err != nil {
				return err
			}
			thread = createThread(req, id)
			err = c.threads.Create(txCtx, thread)
			if err != nil {
				return err
			}
		}
		result.Thread = thread
		if req.Input == nil {
			return nil
		}
		message, err := c.createInput(txCtx, thread.ThreadID, req.Input)
		if err != nil {
			return err
		}
		thread.PendingInputs++
		result.Message = message
		return nil
	})
	if err != nil {
		return agentmodel.ThreadMessageResult{}, err
	}
	return result, nil
}

func (c *Manager) AckInput(ctx context.Context, threadID int64, leaseToken, runID string, ids []int64) (delivered []*agentmodel.MailboxMessage, err error) {
	if len(ids) == 0 {
		return nil, nil
	}
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		thread, err := c.lockThread(txCtx, threadID)
		if err != nil {
			return err
		}
		if !thread.OwnsLease(leaseToken, time.Now()) {
			return ErrLeaseMismatch
		}
		messages, err := c.messages.Get(txCtx, &agentmodel.MailboxMessageFilter{ThreadIDs: []int64{threadID}, IDs: ids, Primary: true})
		if err != nil {
			return err
		}
		if len(messages) != len(ids) {
			return InputErrMessageNotFound
		}
		if runID != "" {
			err = c.ensureRun(txCtx, thread, runID)
			if err != nil {
				return err
			}
		}
		for _, message := range messages {
			if message.Status == agentmodel.MessageStatusCanceled {
				continue
			}
			if message.OutputKey != nil {
				return InputErrMessageNotFound
			}
			if message.Status == agentmodel.MessageStatusAccepted && message.TriggerRunID != "" && message.TriggerRunID != runID {
				return errors.New("input belongs to another Run")
			}
			message.Status = agentmodel.MessageStatusAccepted
			if runID != "" {
				message.TriggerRunID = runID
			}
			_, err = c.messages.Update(txCtx, &agentmodel.MailboxMessageFilter{ThreadIDs: []int64{threadID}, IDs: []int64{message.MessageID}}, map[string]any{"status": message.Status, "trigger_turn_id": message.TriggerRunID})
			if err != nil {
				return err
			}
			delivered = append(delivered, message)
		}
		return nil
	})
	return delivered, err
}

func (c *Manager) Resume(ctx context.Context, threadID int64, input *agentmodel.InputMessage) (result agentmodel.ThreadMessageResult, err error) {
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		thread, err := c.lockThread(txCtx, threadID)
		if err != nil {
			return err
		}
		if thread.Status != agentmodel.ThreadStatusOpen {
			return ErrThreadClosed
		}
		run := thread.LastRun
		if run == nil || run.Status != agentmodel.RunStatusBlocked {
			return ErrThreadNotBlocked
		}
		result.Thread = thread
		if input == nil {
			run.Status = agentmodel.RunStatusInterrupted
			return c.runs.Save(txCtx, run)
		}
		var resume agentmodel.ResumeRunPayload
		err = json.Unmarshal(input.Payload, &resume)
		if err != nil {
			return err
		}
		err = resume.Validate()
		if err != nil {
			return err
		}
		if input.MessageType != agentmodel.MessageTypeResume || resume.RunID != run.RunID || resume.CheckpointID != run.CheckpointID || resume.InterruptID != run.InterruptID {
			return ErrThreadNotBlocked
		}
		// Queue the answer during cleanup; never clear a live Worker's lease.
		var count int64
		err = c.db.DB(txCtx, true).Model(&agentmodel.MailboxMessage{}).Where("thread_id = ? AND message_type = ? AND status = ?", threadID, agentmodel.MessageTypeResume, agentmodel.MessageStatusPending).Count(&count).Error
		if err != nil {
			return err
		}
		if count > 0 {
			return errors.New("resume is already queued")
		}
		err = c.rememberToolApprovals(txCtx, thread, resume)
		if err != nil {
			return err
		}
		message, err := c.createInput(txCtx, threadID, input)
		if err != nil {
			return err
		}
		thread.PendingInputs++
		result.Message = message
		return nil
	})
	if err != nil {
		return agentmodel.ThreadMessageResult{}, err
	}
	return result, nil
}

func (c *Manager) Cancel(ctx context.Context, threadID int64, reason string, cutoffMessageID *int64) (result *agentmodel.ThreadMessageResult, err error) {
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		ctx = txCtx
		thread, err := c.lockThread(ctx, threadID)
		if err != nil {
			return err
		}
		if thread.Status == agentmodel.ThreadStatusClosing || thread.Status == agentmodel.ThreadStatusClosed {
			return ErrThreadClosed
		}
		if thread.LastRun != nil && thread.LastRun.Status == agentmodel.RunStatusBlocked {
			return ErrThreadBlocked
		}
		if reason == "" {
			reason = DefaultCancelInputReason
		}

		var cancelUntilMessageID int64
		if cutoffMessageID != nil {
			cancelUntilMessageID = *cutoffMessageID
			if cancelUntilMessageID <= 0 {
				return fmt.Errorf("%w: cutoff_message_id must be positive", ErrInvalidCancel)
			}
			message, err := c.findMessage(ctx, threadID, cancelUntilMessageID)
			if err != nil {
				return fmt.Errorf("%w: cutoff_message_id=%d", ErrInvalidCancel, cancelUntilMessageID)
			}
			if message.IsControl() {
				return fmt.Errorf("%w: cutoff_message_id=%d is control message", ErrInvalidCancel, cancelUntilMessageID)
			}
		} else {
			messages, err := c.messages.Get(ctx, &agentmodel.MailboxMessageFilter{ThreadIDs: []int64{threadID}, InputOnly: true, Primary: true, Desc: true, Limit: 1})
			if err != nil {
				return err
			}
			if len(messages) > 0 {
				cancelUntilMessageID = messages[0].MessageID
			}
		}
		if cancelUntilMessageID == 0 {
			result = &agentmodel.ThreadMessageResult{Thread: thread}
			return nil
		}
		err = c.cancelQueuedInputsUntil(ctx, threadID, cancelUntilMessageID)
		if err != nil {
			return err
		}

		messageID, err := cache.GenerateID(ctx, c.redis)
		if err != nil {
			return err
		}
		requestID := strconv.FormatInt(messageID, 10)
		metadata := map[string]string{"control_type": agentmodel.ControlTypeCancelInput, "request_id": requestID, "cutoff_message_id": strconv.FormatInt(cancelUntilMessageID, 10)}
		if reason != "" {
			metadata["reason"] = reason
		}
		payload, err := json.Marshal(agentmodel.CancelInputControlPayload{ControlType: agentmodel.ControlTypeCancelInput, RequestID: requestID, ThreadID: threadID, CutoffMessageID: cancelUntilMessageID, Reason: reason})
		if err != nil {
			return err
		}
		controlMessage := &agentmodel.MailboxMessage{
			MessageID:   messageID,
			ThreadID:    threadID,
			Sender:      &agentmodel.MailboxSender{Type: agentmodel.MailboxSenderTypeSystem, ID: agentmodel.RecordAgentManagerSenderID},
			MessageType: agentmodel.ControlMessageTypeCancelInput, Status: agentmodel.MessageStatusPending,
			Payload:   payload,
			Metadata:  metadata,
			CreatedAt: time.Now(),
		}
		err = c.messages.Create(ctx, controlMessage)
		if err != nil {
			return err
		}

		thread.PendingInputs++
		result = &agentmodel.ThreadMessageResult{Thread: thread, Message: controlMessage}
		return nil
	})
	return result, err
}

// ListMessages 查询历史消息。
func (c *Manager) ListMessages(ctx context.Context, req agentmodel.ListMessagesRequest) (result agentmodel.ListMessagesResult, err error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	filter := &agentmodel.MailboxMessageFilter{
		Primary: true, SkipNormalize: true, ExcludeControls: true,
		SessionID: req.SessionID, RunID: req.RunID,
		Offset: max(req.Offset, 0), Limit: int(min(limit, 1000)), Desc: req.Backward,
	}
	if req.AfterID > 0 {
		filter.AfterID = &req.AfterID
	}
	if req.ThreadID != 0 {
		filter.ThreadIDs = []int64{req.ThreadID}
	}
	countFilter := *filter
	countFilter.Total = &result.Total
	_, err = c.messages.Get(ctx, &countFilter)
	if err != nil {
		return result, err
	}
	result.Messages, err = c.messages.Get(ctx, filter)
	if err != nil {
		return result, err
	}
	ids := make([]string, 0, len(result.Messages))
	for _, message := range result.Messages {
		ids = append(ids, message.TriggerRunID)
	}
	// A session query may span multiple Threads; RunIDs are globally unique.
	var rows []*agentmodel.RunRecord
	err = c.db.DB(ctx, true).Where("run_id IN ?", ids).Find(&rows).Error
	if err != nil {
		return result, err
	}
	result.Runs = make(map[string]*agentmodel.RunRecord, len(rows))
	for _, run := range rows {
		result.Runs[run.RunID] = run
	}
	return result, nil
}

func (c *Manager) createInput(ctx context.Context, threadID int64, input *agentmodel.InputMessage) (*agentmodel.MailboxMessage, error) {
	id, err := cache.GenerateID(ctx, c.redis)
	if err != nil {
		return nil, err
	}
	message := &agentmodel.MailboxMessage{
		MessageID: id, ThreadID: threadID, CreatedAt: time.Now(),
		Sender:      &agentmodel.MailboxSender{Type: agentmodel.RecordNormalizeSenderType(input.SenderType), ID: input.SenderID},
		MessageType: input.MessageType, Status: agentmodel.MessageStatusPending,
		Payload: append([]byte(nil), input.Payload...), Metadata: input.Metadata,
	}
	if input.MessageType == agentmodel.MessageTypeResume {
		var resume agentmodel.ResumeRunPayload
		err = json.Unmarshal(input.Payload, &resume)
		if err != nil {
			return nil, err
		}
		message.TriggerRunID = resume.RunID
	}
	err = c.messages.Create(ctx, message)
	return message, err
}

// Ack can arrive before RunStart is drained. Persist its Run identity here too.
func (c *Manager) ensureRun(ctx context.Context, thread *agentmodel.ThreadRecord, runID string) error {
	runs, err := c.runs.Get(ctx, thread.ThreadID, []string{runID})
	if err != nil {
		return err
	}
	run := runs[runID]
	if run == nil {
		run = &agentmodel.RunRecord{RunID: runID, ThreadID: thread.ThreadID, Status: agentmodel.RunStatusStarted, LeaseToken: thread.LeaseToken}
		err = c.runs.Save(ctx, run)
		if err != nil {
			return err
		}
	}
	if !run.Ended() && (thread.LastRun == nil || thread.LastRun.Ended()) {
		_, err = c.threads.Update(ctx, &agentmodel.ThreadFilter{IDs: []int64{thread.ThreadID}}, map[string]any{"last_run_id": runID})
		if err != nil {
			return err
		}
		thread.LastRunID, thread.LastRun = runID, run
	}
	return nil
}

func (c *Manager) findMessage(ctx context.Context, threadID, messageID int64) (message *agentmodel.MailboxMessage, err error) {
	if threadID <= 0 || messageID <= 0 {
		return nil, ErrMessageNotFound
	}
	messages, err := c.messages.Get(ctx, &agentmodel.MailboxMessageFilter{ThreadIDs: []int64{threadID}, IDs: []int64{messageID}, Take: true, Primary: true})
	if errors.Is(err, db.MySQLErrRecordNotFound) {
		return nil, ErrMessageNotFound
	}
	if err != nil {
		return nil, err
	}
	return messages[0], nil
}

func (c *Manager) cancelQueuedInputsUntil(ctx context.Context, threadID int64, cutoff int64) error {
	messages, err := c.messages.Get(ctx, &agentmodel.MailboxMessageFilter{ThreadIDs: []int64{threadID}, InputOnly: true, Statuses: []string{agentmodel.MessageStatusPending}, Primary: true})
	if err != nil {
		return err
	}
	ids := make([]int64, 0, len(messages))
	for _, message := range messages {
		if message.MessageID <= cutoff {
			ids = append(ids, message.MessageID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	_, err = c.messages.Update(ctx, &agentmodel.MailboxMessageFilter{ThreadIDs: []int64{threadID}, IDs: ids}, map[string]any{"status": agentmodel.MessageStatusCanceled})
	return err
}
