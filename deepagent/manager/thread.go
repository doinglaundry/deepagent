package manager

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"eino-cli/deepagent/dal/db"
	"eino-cli/deepagent/dal/model"
	eventpkg "eino-cli/deepagent/protocol/event"
	inputpkg "eino-cli/deepagent/protocol/input"

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

type AcquireRequest struct {
	ThreadID   int64
	LeaseToken string
	LeaseMS    int64
	ScanLimit  int32
}

type Lease struct {
	ThreadID   int64     `json:"thread_id"`
	LeaseToken string    `json:"lease_token"`
	LeaseUntil time.Time `json:"lease_until"`
}

type AcquireResult struct {
	Thread          *model.Thread
	Lease           *Lease
	PendingMessages []*db.Message
	ServerTimeMS    int64
}

type ThreadMessageResult struct {
	Thread  *model.Thread
	Message *db.Message
}

type ListThreadsRequest struct {
	ThreadID  int64
	SessionID string
	Limit     int32
	Offset    int
}

type ListThreadsResult struct {
	Thread  *model.Thread
	Threads []*model.Thread
	Total   int64
}

type CloseThreadControlPayload struct {
	ControlType string `json:"control_type"`
	RequestID   string `json:"request_id"`
	ThreadID    int64  `json:"thread_id"`
	Reason      string `json:"reason,omitempty"`
}

// Acquire fetches input for an owned Thread, or leases a Thread with durable work.
func (c *Manager) Acquire(ctx context.Context, req AcquireRequest) (result AcquireResult, err error) {
	now := time.Now()
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		var thread *model.Thread
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
			rows, err := c.threads.Get(txCtx, &model.ThreadFilter{RunnableUntil: &now, Primary: true, ForUpdate: true, Limit: 1})
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
			_, err = c.threads.Update(txCtx, &model.ThreadFilter{IDs: []int64{thread.ThreadID}}, map[string]any{"lease_token": token, "lease_until": until})
			if err != nil {
				return err
			}
			thread.LeaseToken, thread.LeaseUntil = token, &until
			result.Thread = thread
			result.Lease = &Lease{ThreadID: thread.ThreadID, LeaseToken: token, LeaseUntil: until}
		}
		blocked := thread.LastRun != nil && thread.LastRun.Status == eventpkg.RunStatusBlocked
		messages, err := c.messages.Get(txCtx, &model.MessageFilter{
			ThreadIDs: []int64{thread.ThreadID}, Statuses: []string{model.MessageStatusPending},
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
		_, err = c.threads.Update(txCtx, &model.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"lease_until": lease.LeaseUntil})
		return err
	})
	return lease, err
}

// ReleaseThread relinquishes ownership. It does not change lifecycle or execution.
func (c *Manager) ReleaseThread(ctx context.Context, threadID int64, leaseToken string) (thread *model.Thread, err error) {
	err = c.db.Transaction(ctx, func(txCtx context.Context) error {
		var err error
		thread, err = c.lockThread(txCtx, threadID)
		if err != nil {
			return err
		}
		if !thread.OwnsLease(leaseToken, time.Now()) {
			return ErrLeaseMismatch
		}
		_, err = c.threads.Update(txCtx, &model.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"lease_token": "", "lease_until": nil})
		if err != nil {
			return err
		}
		thread.LeaseToken, thread.LeaseUntil = "", nil
		return nil
	})
	return thread, err
}

// ListThreads 查询 Thread；传 ThreadID 查单个，传 SessionID 查列表。
func (c *Manager) ListThreads(ctx context.Context, req ListThreadsRequest) (result ListThreadsResult, err error) {
	if req.ThreadID != 0 {
		rows, err := c.threads.Get(ctx, &model.ThreadFilter{IDs: []int64{req.ThreadID}})
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
	filter := &model.ThreadFilter{SessionIDs: []string{req.SessionID}, Offset: max(req.Offset, 0), Limit: int(min(limit, maxScanLimit))}
	countFilter := *filter
	countFilter.Total = &result.Total
	_, err = c.threads.Get(ctx, &countFilter)
	if err != nil {
		return ListThreadsResult{}, err
	}
	result.Threads, err = c.threads.Get(ctx, filter)
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

		if thread.Status == model.ThreadStatusClosed {
			result = &ThreadMessageResult{Thread: thread}
			return nil
		}
		// With no owner, there are no live resources to drain.
		if thread.LeaseToken == "" && thread.Status == model.ThreadStatusOpen {
			err = c.cancelQueuedInputsUntil(ctx, threadID, int64(1<<63-1))
			if err != nil {
				return err
			}
			_, err = c.threads.Update(ctx, &model.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"status": model.ThreadStatusClosed, "lease_until": nil})
			if err != nil {
				return err
			}
			thread.Status, thread.LeaseUntil = model.ThreadStatusClosed, nil
			result = &ThreadMessageResult{Thread: thread}
			return nil
		}
		_, err = c.threads.Update(ctx, &model.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"status": model.ThreadStatusClosing})
		if err != nil {
			return err
		}
		thread.Status = model.ThreadStatusClosing

		err = c.cancelQueuedInputsUntil(ctx, threadID, int64(1<<63-1))
		if err != nil {
			return err
		}

		pending, err := c.messages.Get(ctx, &model.MessageFilter{ThreadIDs: []int64{threadID}, Statuses: []string{model.MessageStatusPending}, Primary: true, PriorityFirst: true})
		if err != nil {
			return err
		}
		for _, message := range pending {
			if message.Status == model.MessageStatusPending && message.IsCloseControl() {
				result = &ThreadMessageResult{Thread: thread, Message: message}
				return nil
			}
		}

		messageID, err := IDNextSharedID(ctx, c.redis)
		if err != nil {
			return err
		}
		requestID := strconv.FormatInt(messageID, 10)
		metadata := map[string]string{"control_type": model.ControlTypeCloseThread, "request_id": requestID}
		if reason != "" {
			metadata["reason"] = reason
		}
		payload, err := json.Marshal(CloseThreadControlPayload{ControlType: model.ControlTypeCloseThread, RequestID: requestID, ThreadID: threadID, Reason: reason})
		if err != nil {
			return err
		}
		controlMessage := &model.Message{
			MessageID:   messageID,
			ThreadID:    threadID,
			Sender:      &model.Sender{Type: model.SenderTypeSystem, ID: model.RecordAgentManagerSenderID},
			MessageType: model.ControlMessageTypeCloseThread,
			Status:      model.MessageStatusPending,
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
		if thread.Status != model.ThreadStatusClosed {
			if thread.Status != model.ThreadStatusClosing {
				return ErrInvalidClose
			}
			if !thread.OwnsLease(leaseToken, time.Now()) {
				return ErrLeaseMismatch
			}
			_, err = c.threads.Update(txCtx, &model.ThreadFilter{IDs: []int64{threadID}}, map[string]any{"status": model.ThreadStatusClosed, "lease_until": nil, "lease_token": ""})
			if err != nil {
				return err
			}
			thread.Status, thread.LeaseToken, thread.LeaseUntil = model.ThreadStatusClosed, "", nil
		}
		_, err = c.messages.Update(txCtx, &model.MessageFilter{ThreadIDs: []int64{threadID}, IDs: []int64{controlMessageID}}, map[string]any{"status": model.MessageStatusAccepted})
		if err != nil {
			return err
		}
		message.Status = model.MessageStatusAccepted
		result = &ThreadMessageResult{Thread: thread, Message: message}
		return nil
	})
	return result, err
}

func (c *Manager) lockThread(ctx context.Context, threadID int64) (*model.Thread, error) {
	rows, err := c.threads.Get(ctx, &model.ThreadFilter{IDs: []int64{threadID}, Primary: true, ForUpdate: true})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrThreadNotFound
	}
	return rows[0], nil
}

// Recovery requeues unfinished delivery; blocked Runs wait for their current answer.
func (c *Manager) recoverAcceptedInputs(ctx context.Context, thread *model.Thread) error {
	messages, err := c.messages.Get(ctx, &model.MessageFilter{ThreadIDs: []int64{thread.ThreadID}, Statuses: []string{model.MessageStatusPending, model.MessageStatusAccepted}, Primary: true})
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
		if message.Status != model.MessageStatusAccepted || message.IsControl() {
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
		_, err = c.messages.Update(ctx, &model.MessageFilter{ThreadIDs: []int64{thread.ThreadID}, IDs: []int64{message.MessageID}}, map[string]any{"status": model.MessageStatusPending, "trigger_turn_id": runID})
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
