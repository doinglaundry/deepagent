package manager

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"eino-cli/deepagent/dal/cache"
	agentmodel "eino-cli/deepagent/model"

	"github.com/google/uuid"
)

const (
	defaultLeaseDuration     = time.Minute
	maxLeaseDuration         = 30 * time.Minute
	queuedMessageLimit       = 10
	defaultScanLimit         = int32(50)
	maxScanLimit             = int32(100)
	DefaultCancelInputReason = "user_cancel"
	DefaultCloseThreadReason = "user_close"
)

// Acquire fetches input for an owned Thread, or leases a Thread with durable work.
func (c *Manager) Acquire(ctx context.Context, req agentmodel.AcquireRequest) (result agentmodel.AcquireResult, err error) {
	now := time.Now()
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		var thread *agentmodel.ThreadRecord
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
			rows, err := c.threads.Get(txCtx, &agentmodel.ThreadFilter{RunnableUntil: &now, Primary: true, ForUpdate: true, Limit: 1})
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
			_, err = c.threads.Update(txCtx, &agentmodel.ThreadFilter{IDs: []int64{thread.ThreadID}}, map[string]any{"lease_token": token, "lease_until": until})
			if err != nil {
				return err
			}
			thread.LeaseToken, thread.LeaseUntil = token, &until
			result.Thread = thread
			result.Lease = &agentmodel.Lease{ThreadID: thread.ThreadID, LeaseToken: token, LeaseUntil: until}
		}
		blocked := thread.LastRun != nil && thread.LastRun.Status == agentmodel.RunStatusBlocked
		messages, err := c.messages.Get(txCtx, &agentmodel.MailboxMessageFilter{
			ThreadIDs: []int64{thread.ThreadID}, Statuses: []string{agentmodel.MessageStatusPending},
			Primary: true, PriorityFirst: true, PriorityOnly: blocked, Limit: queuedMessageLimit,
		})
		result.PendingMessages, result.ServerTimeMS = messages, now.UnixMilli()
		return err
	})
	if errors.Is(err, ErrThreadNotRunnable) {
		return agentmodel.AcquireResult{}, nil
	}
	return result, err
}

func (c *Manager) Renew(ctx context.Context, threadID int64, leaseToken string, leaseMS int64) (lease *agentmodel.Lease, err error) {
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		thread, err := c.lockThread(txCtx, threadID)
		if err != nil {
			return err
		}
		now := time.Now()
		if !thread.OwnsLease(leaseToken, now) {
			return ErrLeaseMismatch
		}
		lease = &agentmodel.Lease{ThreadID: threadID, LeaseToken: leaseToken, LeaseUntil: now.Add(normalizeLeaseDuration(leaseMS))}
		_, err = c.threads.Update(txCtx, &agentmodel.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"lease_until": lease.LeaseUntil})
		return err
	})
	return lease, err
}

// ReleaseThread relinquishes ownership. It does not change lifecycle or execution.
func (c *Manager) ReleaseThread(ctx context.Context, threadID int64, leaseToken string) (thread *agentmodel.ThreadRecord, err error) {
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		var err error
		thread, err = c.lockThread(txCtx, threadID)
		if err != nil {
			return err
		}
		if !thread.OwnsLease(leaseToken, time.Now()) {
			return ErrLeaseMismatch
		}
		_, err = c.threads.Update(txCtx, &agentmodel.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"lease_token": "", "lease_until": nil})
		if err != nil {
			return err
		}
		thread.LeaseToken, thread.LeaseUntil = "", nil
		return nil
	})
	return thread, err
}

// ListThreads 查询 Thread；传 ThreadID 查单个，传 SessionID 查列表。
func (c *Manager) ListThreads(ctx context.Context, req agentmodel.ListThreadsRequest) (result agentmodel.ListThreadsResult, err error) {
	if req.ThreadID != 0 {
		rows, err := c.threads.Get(ctx, &agentmodel.ThreadFilter{IDs: []int64{req.ThreadID}})
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
	filter := &agentmodel.ThreadFilter{SessionIDs: []string{req.SessionID}, Offset: max(req.Offset, 0), Limit: int(min(limit, maxScanLimit))}
	countFilter := *filter
	countFilter.Total = &result.Total
	_, err = c.threads.Get(ctx, &countFilter)
	if err != nil {
		return agentmodel.ListThreadsResult{}, err
	}
	result.Threads, err = c.threads.Get(ctx, filter)
	return result, err
}

func (c *Manager) Close(ctx context.Context, threadID int64, reason string) (result *agentmodel.ThreadMessageResult, err error) {
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		ctx = txCtx
		thread, err := c.lockThread(ctx, threadID)
		if err != nil {
			return err
		}
		if reason == "" {
			reason = DefaultCloseThreadReason
		}

		if thread.Status == agentmodel.ThreadStatusClosed {
			result = &agentmodel.ThreadMessageResult{Thread: thread}
			return nil
		}
		// With no owner, there are no live resources to drain.
		if thread.LeaseToken == "" && thread.Status == agentmodel.ThreadStatusOpen {
			err = c.cancelQueuedInputsUntil(ctx, threadID, int64(1<<63-1))
			if err != nil {
				return err
			}
			_, err = c.threads.Update(ctx, &agentmodel.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"status": agentmodel.ThreadStatusClosed, "lease_until": nil})
			if err != nil {
				return err
			}
			thread.Status, thread.LeaseUntil = agentmodel.ThreadStatusClosed, nil
			result = &agentmodel.ThreadMessageResult{Thread: thread}
			return nil
		}
		_, err = c.threads.Update(ctx, &agentmodel.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"status": agentmodel.ThreadStatusClosing})
		if err != nil {
			return err
		}
		thread.Status = agentmodel.ThreadStatusClosing

		err = c.cancelQueuedInputsUntil(ctx, threadID, int64(1<<63-1))
		if err != nil {
			return err
		}

		pending, err := c.messages.Get(ctx, &agentmodel.MailboxMessageFilter{ThreadIDs: []int64{threadID}, Statuses: []string{agentmodel.MessageStatusPending}, Primary: true, PriorityFirst: true})
		if err != nil {
			return err
		}
		for _, message := range pending {
			if message.Status == agentmodel.MessageStatusPending && message.IsCloseControl() {
				result = &agentmodel.ThreadMessageResult{Thread: thread, Message: message}
				return nil
			}
		}

		messageID, err := cache.GenerateID(ctx, c.redis)
		if err != nil {
			return err
		}
		requestID := strconv.FormatInt(messageID, 10)
		metadata := map[string]string{"control_type": agentmodel.ControlTypeCloseThread, "request_id": requestID}
		if reason != "" {
			metadata["reason"] = reason
		}
		payload, err := json.Marshal(agentmodel.CloseThreadControlPayload{ControlType: agentmodel.ControlTypeCloseThread, RequestID: requestID, ThreadID: threadID, Reason: reason})
		if err != nil {
			return err
		}
		controlMessage := &agentmodel.MailboxMessage{
			MessageID:   messageID,
			ThreadID:    threadID,
			Sender:      &agentmodel.MailboxSender{Type: agentmodel.MailboxSenderTypeSystem, ID: agentmodel.RecordAgentManagerSenderID},
			MessageType: agentmodel.ControlMessageTypeCloseThread,
			Status:      agentmodel.MessageStatusPending,
			Payload:     payload,
			Metadata:    metadata,
			CreatedAt:   time.Now(),
		}
		err = c.messages.Create(ctx, controlMessage)
		if err != nil {
			return err
		}
		result = &agentmodel.ThreadMessageResult{Thread: thread, Message: controlMessage}
		return nil
	})
	return result, err
}

func (c *Manager) ConfirmThreadClosed(ctx context.Context, threadID int64, leaseToken string, controlMessageID int64) (result *agentmodel.ThreadMessageResult, err error) {
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
		if thread.Status != agentmodel.ThreadStatusClosed {
			if thread.Status != agentmodel.ThreadStatusClosing {
				return ErrInvalidClose
			}
			if !thread.OwnsLease(leaseToken, time.Now()) {
				return ErrLeaseMismatch
			}
			_, err = c.threads.Update(txCtx, &agentmodel.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"status": agentmodel.ThreadStatusClosed, "lease_until": nil, "lease_token": ""})
			if err != nil {
				return err
			}
			thread.Status, thread.LeaseToken, thread.LeaseUntil = agentmodel.ThreadStatusClosed, "", nil
		}
		_, err = c.messages.Update(txCtx, &agentmodel.MailboxMessageFilter{ThreadIDs: []int64{threadID}, IDs: []int64{controlMessageID}}, map[string]any{"status": agentmodel.MessageStatusAccepted})
		if err != nil {
			return err
		}
		message.Status = agentmodel.MessageStatusAccepted
		result = &agentmodel.ThreadMessageResult{Thread: thread, Message: message}
		return nil
	})
	return result, err
}

func (c *Manager) lockThread(ctx context.Context, threadID int64) (*agentmodel.ThreadRecord, error) {
	rows, err := c.threads.Get(ctx, &agentmodel.ThreadFilter{IDs: []int64{threadID}, Primary: true, ForUpdate: true})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrThreadNotFound
	}
	return rows[0], nil
}

// Recovery requeues unfinished delivery; blocked Runs wait for their current answer.
func (c *Manager) recoverAcceptedInputs(ctx context.Context, thread *agentmodel.ThreadRecord) error {
	messages, err := c.messages.Get(ctx, &agentmodel.MailboxMessageFilter{ThreadIDs: []int64{thread.ThreadID}, Statuses: []string{agentmodel.MessageStatusPending, agentmodel.MessageStatusAccepted}, Primary: true})
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
		if message.MessageType != agentmodel.MessageTypeResume || run == nil || run.Ended() {
			continue
		}
		var answer agentmodel.ResumeRunPayload
		err = json.Unmarshal(message.Payload, &answer)
		if err != nil {
			return err
		}
		if answer.InterruptID == run.InterruptID {
			resumeRuns[run.RunID] = true
		}
	}
	for _, message := range messages {
		if message.Status != agentmodel.MessageStatusAccepted || message.IsControl() {
			continue
		}
		run := runs[message.TriggerRunID]
		if run.Ended() {
			continue
		}
		if resumeRuns[message.TriggerRunID] {
			if message.MessageType == agentmodel.MessageTypeResume {
				var answer agentmodel.ResumeRunPayload
				err = json.Unmarshal(message.Payload, &answer)
				if err != nil {
					return err
				}
				if answer.InterruptID != run.InterruptID {
					continue
				}
			}
		} else if run != nil && run.Status == agentmodel.RunStatusBlocked {
			continue
		}
		runID := ""
		if message.MessageType == agentmodel.MessageTypeResume {
			runID = message.TriggerRunID
		}
		_, err = c.messages.Update(ctx, &agentmodel.MailboxMessageFilter{ThreadIDs: []int64{thread.ThreadID}, IDs: []int64{message.MessageID}}, map[string]any{"status": agentmodel.MessageStatusPending, "trigger_turn_id": runID})
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
	if thread.LastRun != nil && thread.LastRun.Status == agentmodel.RunStatusStarted && !resumeRuns[thread.LastRunID] {
		thread.LastRun.Status = agentmodel.RunStatusInterrupted
	}

	return nil
}
