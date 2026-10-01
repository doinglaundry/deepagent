package manager

import (
	"context"
	dalcache "eino-cli/deepagent/dal/cache"
	daldb "eino-cli/deepagent/dal/db"
	dalmodel "eino-cli/deepagent/dal/model"
	eventpkg "eino-cli/deepagent/protocol/event"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	inputpkg "eino-cli/deepagent/protocol/input"
	"github.com/google/uuid"
)

type Manager struct {
	threads                 *daldb.ThreadDAO
	messages                *daldb.MessageDAO
	runs                    *daldb.RunDAO
	redis                   dalcache.RedisClient
	db                      *daldb.MySQLClient
	stream                  *StreamStreamOut
	subscribeSessionMaxIdle time.Duration
}

func (c *Manager) Submit(ctx context.Context, req SubmitRequest) (result ThreadMessageResult, err error) {
	if req.ThreadID != 0 && req.Input == nil {
		return result, errors.New("input is required")
	}
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		var thread *dalmodel.Thread
		if req.ThreadID != 0 {
			var err error
			thread, err = c.lockThread(txCtx, req.ThreadID)
			if err != nil {
				return err
			}
			if thread.Status != dalmodel.ThreadStatusOpen {
				return ErrThreadClosed
			}
		} else {
			id, err := IDNextSharedID(txCtx, c.redis)
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
		return ThreadMessageResult{}, err
	}
	return result, nil
}

// Acquire fetches input for an owned Thread, or leases a Thread with durable work.
func (c *Manager) Acquire(ctx context.Context, req AcquireRequest) (result AcquireResult, err error) {
	now := time.Now()
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		var thread *dalmodel.Thread
		if req.ThreadID != 0 {
			var err error
			thread, err = c.lockThread(txCtx, req.ThreadID)
			if err != nil {
				return err
			}
			now = time.Now()
			if !thread.OwnsLease(req.LeaseToken, now) {
				return ErrLeaseMismatch
			}
		} else {
			rows, err := c.threads.Get(txCtx, &dalmodel.ThreadFilter{RunnableUntil: &now, Primary: true, ForUpdate: true, Limit: 1})
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				return ErrThreadNotRunnable
			}
			thread = rows[0]
			err = c.recoverAcceptedInputs(txCtx, thread)
			if err != nil {
				return err
			}
			now = time.Now()
			until := now.Add(normalizeLeaseDuration(req.LeaseMS))
			token := uuid.NewString()
			_, err = c.threads.Update(txCtx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}}, map[string]any{"lease_token": token, "lease_until": until})
			if err != nil {
				return err
			}
			thread.LeaseToken, thread.LeaseUntil = token, &until
			result.Thread = thread
			result.Lease = &Lease{ThreadID: thread.ThreadID, LeaseToken: token, LeaseUntil: until}
		}
		blocked := thread.LastRun != nil && thread.LastRun.Status == eventpkg.RunStatusBlocked
		messages, err := c.messages.Get(txCtx, &dalmodel.MessageFilter{
			ThreadIDs: []int64{thread.ThreadID}, Statuses: []string{dalmodel.MessageStatusPending},
			Primary: true, PriorityFirst: true, PriorityOnly: blocked, Limit: queuedMessageLimit,
		})
		result.PendingMessages, result.ServerTimeMS = messages, now.UnixMilli()
		return err
	})
	if errors.Is(err, ErrThreadNotRunnable) {
		return AcquireResult{}, nil
	}
	return result, err
}

func (c *Manager) Renew(ctx context.Context, threadID int64, leaseToken string, leaseMS int64) (lease *Lease, err error) {
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		thread, err := c.lockThread(txCtx, threadID)
		if err != nil {
			return err
		}
		now := time.Now()
		if !thread.OwnsLease(leaseToken, now) {
			return ErrLeaseMismatch
		}
		lease = &Lease{ThreadID: threadID, LeaseToken: leaseToken, LeaseUntil: now.Add(normalizeLeaseDuration(leaseMS))}
		_, err = c.threads.Update(txCtx, &dalmodel.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"lease_until": lease.LeaseUntil})
		return err
	})
	return lease, err
}

// ListThreads 查询 Thread；传 ThreadID 查单个，传 SessionID 查列表。
func (c *Manager) ListThreads(ctx context.Context, req ListThreadsRequest) (result ListThreadsResult, err error) {
	if req.ThreadID != 0 {
		rows, err := c.threads.Get(ctx, &dalmodel.ThreadFilter{IDs: []int64{req.ThreadID}})
		if err != nil {
			return result, err
		}
		if len(rows) == 0 {
			return result, ErrThreadNotFound
		}
		result.Thread = rows[0]
		result.Threads = rows
		result.Total = int64(len(rows))
		return result, nil
	}

	limit := req.Limit
	if limit <= 0 {
		limit = defaultScanLimit
	}
	filter := &dalmodel.ThreadFilter{SessionIDs: []string{req.SessionID}, Offset: max(req.Offset, 0), Limit: int(min(limit, maxScanLimit))}
	countFilter := *filter
	countFilter.Total = &result.Total
	_, err = c.threads.Get(ctx, &countFilter)
	if err != nil {
		return ListThreadsResult{}, err
	}
	result.Threads, err = c.threads.Get(ctx, filter)
	return result, err
}

// SubscribeSession 建立 Session 实时订阅。
func (c *Manager) SubscribeSession(ctx context.Context, sessionID, recoverQueueID string) (subscription *Subscription, err error) {
	if c == nil || c.stream == nil {
		return nil, errors.New("manager stream is unavailable")
	}
	subscription = newSubscription(ctx, c.stream, SubscribeSessionRequest{SessionID: sessionID, RecoverQueueID: recoverQueueID}, c.subscribeSessionMaxIdle)
	return subscription, subscription.Err
}

// ListMessages 查询历史消息。
func (c *Manager) ListMessages(ctx context.Context, req ListMessagesRequest) (result ListMessagesResult, err error) {
	limit := req.Limit
	if limit <= 0 {
		limit = 100
	}
	filter := &dalmodel.MessageFilter{
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
	var rows []*dalmodel.RunRecord
	err = c.db.DB(ctx, true).Where("run_id IN ?", ids).Find(&rows).Error
	if err != nil {
		return result, err
	}
	result.Runs = make(map[string]*dalmodel.RunRecord, len(rows))
	for _, run := range rows {
		result.Runs[run.RunID] = run
	}
	return result, nil
}

func (c *Manager) findMessage(ctx context.Context, threadID, messageID int64) (message *dalmodel.Message, err error) {
	if threadID <= 0 || messageID <= 0 {
		return nil, ErrMessageNotFound
	}
	messages, err := c.messages.Get(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, IDs: []int64{messageID}, Take: true, Primary: true})
	if errors.Is(err, daldb.MySQLErrRecordNotFound) {
		return nil, ErrMessageNotFound
	}
	if err != nil {
		return nil, err
	}
	return messages[0], nil
}

// Recovery requeues unfinished delivery; blocked Runs wait for their current answer.
func (c *Manager) recoverAcceptedInputs(ctx context.Context, thread *dalmodel.Thread) error {
	messages, err := c.messages.Get(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{thread.ThreadID}, Statuses: []string{dalmodel.MessageStatusPending, dalmodel.MessageStatusAccepted}, Primary: true})
	if err != nil {
		return err
	}
	runIDs := make([]string, 0, len(messages))
	for _, message := range messages {
		runIDs = append(runIDs, message.TriggerRunID)
	}
	runs, err := c.runs.Get(ctx, thread.ThreadID, runIDs)
	if err != nil {
		return err
	}
	// Only the answer for the latest interrupt is recoverable. Older answers
	// remain accepted history when the same Run blocks again.
	resumeRuns := make(map[string]bool)
	for _, message := range messages {
		run := runs[message.TriggerRunID]
		if message.MessageType != inputpkg.MessageTypeResume || run == nil || run.Ended() {
			continue
		}
		var answer inputpkg.ResumeRunPayload
		err = json.Unmarshal(message.Payload, &answer)
		if err != nil {
			return err
		}
		if answer.InterruptID == run.InterruptID {
			resumeRuns[run.RunID] = true
		}
	}
	for _, message := range messages {
		if message.Status != dalmodel.MessageStatusAccepted || message.IsControl() {
			continue
		}
		run := runs[message.TriggerRunID]
		if run.Ended() {
			continue
		}
		if resumeRuns[message.TriggerRunID] {
			if message.MessageType == inputpkg.MessageTypeResume {
				var answer inputpkg.ResumeRunPayload
				err = json.Unmarshal(message.Payload, &answer)
				if err != nil {
					return err
				}
				if answer.InterruptID != run.InterruptID {
					continue
				}
			}
		} else if run != nil && run.Status == eventpkg.RunStatusBlocked {
			continue
		}
		runID := ""
		if message.MessageType == inputpkg.MessageTypeResume {
			runID = message.TriggerRunID
		}
		_, err = c.messages.Update(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{thread.ThreadID}, IDs: []int64{message.MessageID}}, map[string]any{"status": dalmodel.MessageStatusPending, "trigger_turn_id": runID})
		if err != nil {
			return err
		}
	}
	// Resume restores the existing checkpoint. Ordinary crashed execution is
	// redelivered into a new Run, with the old outcome retained for history.
	resumed := make([]string, 0, len(resumeRuns))
	for id := range resumeRuns {
		resumed = append(resumed, id)
	}
	err = c.runs.InterruptStarted(ctx, thread.ThreadID, resumed)
	if err != nil {
		return err
	}
	if thread.LastRun != nil && thread.LastRun.Status == eventpkg.RunStatusStarted && !resumeRuns[thread.LastRunID] {
		thread.LastRun.Status = eventpkg.RunStatusInterrupted
	}

	return nil
}

func (c *Manager) Resume(ctx context.Context, threadID int64, input *InputMessage) (result ThreadMessageResult, err error) {
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		thread, err := c.lockThread(txCtx, threadID)
		if err != nil {
			return err
		}
		if thread.Status != dalmodel.ThreadStatusOpen {
			return ErrThreadClosed
		}
		run := thread.LastRun
		if run == nil || run.Status != eventpkg.RunStatusBlocked {
			return ErrThreadNotBlocked
		}
		result.Thread = thread
		if input == nil {
			run.Status = eventpkg.RunStatusInterrupted
			return c.runs.Save(txCtx, run)
		}
		var resume inputpkg.ResumeRunPayload
		err = json.Unmarshal(input.Payload, &resume)
		if err != nil {
			return err
		}
		err = resume.Validate()
		if err != nil {
			return err
		}
		if input.MessageType != inputpkg.MessageTypeResume || resume.RunID != run.RunID || resume.CheckpointID != run.CheckpointID || resume.InterruptID != run.InterruptID {
			return ErrThreadNotBlocked
		}
		// Queue the answer during cleanup; never clear a live Worker's lease.
		var count int64
		err = c.db.DB(txCtx, true).Model(&dalmodel.Message{}).Where("thread_id = ? AND message_type = ? AND status = ?", threadID, inputpkg.MessageTypeResume, dalmodel.MessageStatusPending).Count(&count).Error
		if err != nil {
			return err
		}
		if count > 0 {
			return errors.New("resume is already queued")
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
		return ThreadMessageResult{}, err
	}
	return result, nil
}

// ReleaseThread relinquishes ownership. It does not change lifecycle or execution.
func (c *Manager) ReleaseThread(ctx context.Context, threadID int64, leaseToken string) (thread *dalmodel.Thread, err error) {
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		var err error
		thread, err = c.lockThread(txCtx, threadID)
		if err != nil {
			return err
		}
		if !thread.OwnsLease(leaseToken, time.Now()) {
			return ErrLeaseMismatch
		}
		_, err = c.threads.Update(txCtx, &dalmodel.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"lease_token": "", "lease_until": nil})
		if err != nil {
			return err
		}
		thread.LeaseToken, thread.LeaseUntil = "", nil
		return nil
	})
	return thread, err
}

func (c *Manager) AckInput(ctx context.Context, threadID int64, leaseToken, runID string, ids []int64) (delivered []*dalmodel.Message, err error) {
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
		messages, err := c.messages.Get(txCtx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, IDs: ids, Primary: true})
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
			if message.Status == dalmodel.MessageStatusCanceled {
				continue
			}
			if message.OutputKey != nil {
				return InputErrMessageNotFound
			}
			if message.Status == dalmodel.MessageStatusAccepted && message.TriggerRunID != "" && message.TriggerRunID != runID {
				return errors.New("input belongs to another Run")
			}
			message.Status = dalmodel.MessageStatusAccepted
			if runID != "" {
				message.TriggerRunID = runID
			}
			_, err = c.messages.Update(txCtx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, IDs: []int64{message.MessageID}}, map[string]any{"status": message.Status, "trigger_turn_id": message.TriggerRunID})
			if err != nil {
				return err
			}
			delivered = append(delivered, message)
		}
		return nil
	})
	return delivered, err
}

func (c *Manager) cancelQueuedInputsUntil(ctx context.Context, threadID int64, cutoff int64) error {
	messages, err := c.messages.Get(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, InputOnly: true, Statuses: []string{dalmodel.MessageStatusPending}, Primary: true})
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
	_, err = c.messages.Update(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, IDs: ids}, map[string]any{"status": dalmodel.MessageStatusCanceled})
	return err
}

func (c *Manager) Cancel(ctx context.Context, threadID int64, reason string, cutoffMessageID *int64) (result *ThreadMessageResult, err error) {
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		ctx = txCtx
		thread, err := c.lockThread(ctx, threadID)
		if err != nil {
			return err
		}
		if thread.Status == dalmodel.ThreadStatusClosing || thread.Status == dalmodel.ThreadStatusClosed {
			return ErrThreadClosed
		}
		if thread.LastRun != nil && thread.LastRun.Status == eventpkg.RunStatusBlocked {
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
			messages, err := c.messages.Get(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, InputOnly: true, Primary: true, Desc: true, Limit: 1})
			if err != nil {
				return err
			}
			if len(messages) > 0 {
				cancelUntilMessageID = messages[0].MessageID
			}
		}
		if cancelUntilMessageID == 0 {
			result = &ThreadMessageResult{Thread: thread}
			return nil
		}
		err = c.cancelQueuedInputsUntil(ctx, threadID, cancelUntilMessageID)
		if err != nil {
			return err
		}

		messageID, err := IDNextSharedID(ctx, c.redis)
		if err != nil {
			return err
		}
		requestID := strconv.FormatInt(messageID, 10)
		metadata := map[string]string{"control_type": dalmodel.ControlTypeCancelInput, "request_id": requestID, "cutoff_message_id": strconv.FormatInt(cancelUntilMessageID, 10)}
		if reason != "" {
			metadata["reason"] = reason
		}
		payload, err := json.Marshal(CancelInputControlPayload{ControlType: dalmodel.ControlTypeCancelInput, RequestID: requestID, ThreadID: threadID, CutoffMessageID: cancelUntilMessageID, Reason: reason})
		if err != nil {
			return err
		}
		controlMessage := &dalmodel.Message{
			MessageID:   messageID,
			ThreadID:    threadID,
			Sender:      &dalmodel.Sender{Type: dalmodel.SenderTypeSystem, ID: dalmodel.RecordAgentManagerSenderID},
			MessageType: dalmodel.ControlMessageTypeCancelInput, Status: dalmodel.MessageStatusPending,
			Payload:   payload,
			Metadata:  metadata,
			CreatedAt: time.Now(),
		}
		err = c.messages.Create(ctx, controlMessage)
		if err != nil {
			return err
		}

		thread.PendingInputs++
		result = &ThreadMessageResult{Thread: thread, Message: controlMessage}
		return nil
	})
	return result, err
}

func (c *Manager) Close(ctx context.Context, threadID int64, reason string) (result *ThreadMessageResult, err error) {
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		ctx = txCtx
		thread, err := c.lockThread(ctx, threadID)
		if err != nil {
			return err
		}
		if reason == "" {
			reason = DefaultCloseThreadReason
		}

		if thread.Status == dalmodel.ThreadStatusClosed {
			result = &ThreadMessageResult{Thread: thread}
			return nil
		}
		// With no owner, there are no live resources to drain.
		if thread.LeaseToken == "" && thread.Status == dalmodel.ThreadStatusOpen {
			err = c.cancelQueuedInputsUntil(ctx, threadID, int64(1<<63-1))
			if err != nil {
				return err
			}
			_, err = c.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"status": dalmodel.ThreadStatusClosed, "lease_until": nil})
			if err != nil {
				return err
			}
			thread.Status, thread.LeaseUntil = dalmodel.ThreadStatusClosed, nil
			result = &ThreadMessageResult{Thread: thread}
			return nil
		}
		_, err = c.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"status": dalmodel.ThreadStatusClosing})
		if err != nil {
			return err
		}
		thread.Status = dalmodel.ThreadStatusClosing

		err = c.cancelQueuedInputsUntil(ctx, threadID, int64(1<<63-1))
		if err != nil {
			return err
		}

		pending, err := c.messages.Get(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, Statuses: []string{dalmodel.MessageStatusPending}, Primary: true, PriorityFirst: true})
		if err != nil {
			return err
		}
		for _, message := range pending {
			if message.Status == dalmodel.MessageStatusPending && message.IsCloseControl() {
				result = &ThreadMessageResult{Thread: thread, Message: message}
				return nil
			}
		}

		messageID, err := IDNextSharedID(ctx, c.redis)
		if err != nil {
			return err
		}
		requestID := strconv.FormatInt(messageID, 10)
		metadata := map[string]string{"control_type": dalmodel.ControlTypeCloseThread, "request_id": requestID}
		if reason != "" {
			metadata["reason"] = reason
		}
		payload, err := json.Marshal(CloseThreadControlPayload{ControlType: dalmodel.ControlTypeCloseThread, RequestID: requestID, ThreadID: threadID, Reason: reason})
		if err != nil {
			return err
		}
		controlMessage := &dalmodel.Message{
			MessageID:   messageID,
			ThreadID:    threadID,
			Sender:      &dalmodel.Sender{Type: dalmodel.SenderTypeSystem, ID: dalmodel.RecordAgentManagerSenderID},
			MessageType: dalmodel.ControlMessageTypeCloseThread,
			Status:      dalmodel.MessageStatusPending,
			Payload:     payload,
			Metadata:    metadata,
			CreatedAt:   time.Now(),
		}
		err = c.messages.Create(ctx, controlMessage)
		if err != nil {
			return err
		}
		result = &ThreadMessageResult{Thread: thread, Message: controlMessage}
		return nil
	})
	return result, err
}

func (c *Manager) ConfirmThreadClosed(ctx context.Context, threadID int64, leaseToken string, controlMessageID int64) (result *ThreadMessageResult, err error) {
	if controlMessageID <= 0 {
		return nil, ErrInvalidClose
	}
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		thread, err := c.lockThread(txCtx, threadID)
		if err != nil {
			return err
		}
		message, err := c.findMessage(txCtx, threadID, controlMessageID)
		if err != nil {
			return err
		}
		if !message.IsCloseControl() {
			return ErrInvalidClose
		}
		if thread.Status != dalmodel.ThreadStatusClosed {
			if thread.Status != dalmodel.ThreadStatusClosing {
				return ErrInvalidClose
			}
			if !thread.OwnsLease(leaseToken, time.Now()) {
				return ErrLeaseMismatch
			}
			_, err = c.threads.Update(txCtx, &dalmodel.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"status": dalmodel.ThreadStatusClosed, "lease_until": nil, "lease_token": ""})
			if err != nil {
				return err
			}
			thread.Status, thread.LeaseToken, thread.LeaseUntil = dalmodel.ThreadStatusClosed, "", nil
		}
		_, err = c.messages.Update(txCtx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, IDs: []int64{controlMessageID}}, map[string]any{"status": dalmodel.MessageStatusAccepted})
		if err != nil {
			return err
		}
		message.Status = dalmodel.MessageStatusAccepted
		result = &ThreadMessageResult{Thread: thread, Message: message}
		return nil
	})
	return result, err
}

func (c *Manager) SaveOutput(ctx context.Context, threadID int64, leaseToken, runID string, frames []OutputFrame) (err error) {
	if len(frames) == 0 {
		return nil
	}
	outputs := cloneEvents(frames)
	err = c.db.Transaction(ctx, func(txCtx context.Context) (txErr error) {
		threads, txErr := c.threads.Get(txCtx, &dalmodel.ThreadFilter{IDs: []int64{threadID}, Primary: true, ForUpdate: true})
		if txErr != nil {
			return txErr
		}
		if len(threads) == 0 {
			return ErrThreadNotFound
		}
		owner := threads[0]
		if !owner.OwnsLease(leaseToken, time.Now()) {
			return ErrLeaseMismatch
		}

		for i := range outputs {
			output := &outputs[i]
			originalID, txErr := c.prepareOutput(txCtx, output, threadID, runID, owner.SessionID)
			if txErr != nil {
				return txErr
			}
			payload, txErr := parseOutputPayload(output.Payload)
			if txErr != nil {
				return txErr
			}
			txErr = c.saveRunStatus(txCtx, owner, output, payload)
			if txErr != nil {
				return txErr
			}
			rule := outputEventRuleFor(output.EventType, payload)
			switch rule.action {
			case outputActionUpdateInput:
				txErr = c.updateInputExecution(txCtx, threadID, output, payload)
			case outputActionSaveMessage:
				txErr = c.saveOutputMessage(txCtx, threadID, output, payload, originalID, rule)
			}
			if txErr != nil {
				return txErr
			}
		}
		if !owner.OwnsLease(leaseToken, time.Now()) {
			return ErrLeaseMismatch
		}
		return nil
	})
	if err != nil {
		return err
	}
	if outputs[0].SessionID == "" {
		return nil
	}
	if c.stream == nil {
		return ErrOutputUnavailable
	}
	return c.stream.FanoutEventRecords(ctx, outputs[0].SessionID, outputs)
}

func (c *Manager) prepareOutput(ctx context.Context, output *OutputFrame, threadID int64, runID, sessionID string) (originalID int64, err error) {
	output.ThreadID = threadID
	output.SessionID = sessionID
	if output.RunID == "" {
		output.RunID = runID
	}
	if output.RunID == "" {
		return 0, ErrRunIDRequired
	}
	if output.CreatedAt.IsZero() {
		output.CreatedAt = time.Now()
	}
	originalID = output.EventID
	output.EventID, err = IDNextSharedID(ctx, c.redis)
	return originalID, err
}

func (c *Manager) updateInputExecution(ctx context.Context, threadID int64, output *OutputFrame, payload outputPayload) (err error) {
	ids := make([]int64, 0, len(payload.ConsumedMessageIDs))
	for _, raw := range payload.ConsumedMessageIDs {
		id, parseErr := strconv.ParseInt(raw, 10, 64)
		if parseErr != nil {
			return parseErr
		}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil
	}
	values := map[string]any{"trigger_turn_id": output.RunID, "status": dalmodel.MessageStatusAccepted}
	_, err = c.messages.Update(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, IDs: ids, Statuses: []string{dalmodel.MessageStatusPending, dalmodel.MessageStatusAccepted}}, values)
	return err
}

func (c *Manager) saveOutputMessage(ctx context.Context, threadID int64, output *OutputFrame, payload outputPayload, originalID int64, rule outputEventRule) (err error) {
	key := outputMessageKey(output, payload, originalID, rule)
	if rule.messageType == "tool" {
		err = c.mergeToolOutput(ctx, threadID, key, output)
		if err != nil {
			return err
		}
	}
	return c.messages.Create(ctx, &dalmodel.Message{
		MessageID: output.EventID, ThreadID: threadID, Sender: &dalmodel.Sender{Type: rule.sender, ID: "model"},
		MessageType: rule.messageType, Payload: output.Payload,
		Metadata: output.Metadata, TriggerRunID: output.RunID, CreatedAt: output.CreatedAt, OutputKey: &key,
	})
}

func (c *Manager) mergeToolOutput(ctx context.Context, threadID int64, key string, output *OutputFrame) (err error) {
	previous, err := c.messages.Get(ctx, &dalmodel.MessageFilter{Primary: true, Take: true, SkipNormalize: true, ThreadIDs: []int64{threadID}, OutputKey: &key})
	if errors.Is(err, daldb.MySQLErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var before, after eventpkg.ToolCallEventPayload
	err = json.Unmarshal(previous[0].Payload, &before)
	if err != nil {
		return err
	}
	err = json.Unmarshal(output.Payload, &after)
	if err != nil {
		return err
	}
	if after.ArgumentsJSON == nil {
		after.ArgumentsJSON = before.ArgumentsJSON
	}
	if after.ToolName == "" {
		after.ToolName = before.ToolName
	}
	output.Payload, err = json.Marshal(after)
	return err
}
