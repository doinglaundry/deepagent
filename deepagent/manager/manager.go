package manager

import (
	"context"
	"crypto/sha256"
	dalcache "eino-cli/deepagent/dal/cache"
	daldb "eino-cli/deepagent/dal/db"
	dalmodel "eino-cli/deepagent/dal/model"
	eventpkg "eino-cli/deepagent/protocol/event"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	redispkg "github.com/redis/go-redis/v9"
)

type Manager struct {
	threads                 *daldb.ThreadDAO
	messages                *daldb.MessageDAO
	redis                   dalcache.RedisClient
	db                      *daldb.MySQLClient
	stream                  *StreamStreamOut
	subscribeSessionMaxIdle time.Duration
}

func (c *Manager) Submit(ctx context.Context, req SubmitRequest) (ThreadMessageResult, error) {
	// 已有线程必须有输入。
	if req.ThreadID != 0 && req.Input == nil {
		return ThreadMessageResult{}, fmt.Errorf("input is required")
	}

	var thread *dalmodel.Thread
	if req.ThreadID != 0 {
		rows, err := c.threads.Get(ctx, &dalmodel.ThreadFilter{IDs: []int64{req.ThreadID}, Primary: true})
		if err != nil {
			return ThreadMessageResult{}, err
		}
		if len(rows) == 0 {
			return ThreadMessageResult{}, ErrThreadNotFound
		}
		thread = rows[0]
		if thread.Status == dalmodel.ThreadStatusClosing || thread.Status == dalmodel.ThreadStatusClosed {
			return ThreadMessageResult{}, ErrThreadClosed
		}
	} else {
		threadID, err := IDNextSharedID(ctx, c.redis)
		if err != nil {
			return ThreadMessageResult{}, err
		}
		thread = createThread(req, threadID)
		if err = c.threads.Create(ctx, thread); err != nil {
			return ThreadMessageResult{}, err
		}
	}

	// 新建线程可以不携带输入，直接返回空线程。
	if req.Input == nil {
		return ThreadMessageResult{Thread: thread}, nil
	}

	messageID, err := IDNextSharedID(ctx, c.redis)
	if err != nil {
		return ThreadMessageResult{}, err
	}
	metadata := maps.Clone(req.Input.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	message := &dalmodel.Message{
		MessageID: messageID, ThreadID: thread.ThreadID,
		Sender:      &dalmodel.Sender{Type: dalmodel.RecordNormalizeSenderType(req.Input.SenderType), ID: req.Input.SenderID},
		MessageType: req.Input.MessageType, Status: dalmodel.MessageStatusPending,
		Payload: []byte(string(req.Input.Payload)), Metadata: metadata, CreatedAt: time.Now(),
	}
	if err = c.messages.Create(ctx, message); err != nil {
		return ThreadMessageResult{}, err
	}
	if c.redis == nil {
		return ThreadMessageResult{}, ErrRedisUnavailable
	}
	if _, err = c.redis.ZAdd(ctx, RedisPendingInputKey(message.ThreadID), []redispkg.Z{{Score: float64(message.MessageID), Member: strconv.FormatInt(message.MessageID, 10)}}); err != nil {
		return ThreadMessageResult{}, err
	}

	now := time.Now()
	metadata = maps.Clone(thread.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}
	awakened, err := c.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}, Statuses: []string{dalmodel.ThreadStatusIdle}}, map[string]any{
		"status": dalmodel.ThreadStatusReady, "ready_until": now, "metadata_json": metadata,
	})
	if err != nil {
		_, _ = c.redis.ZRem(ctx, RedisPendingInputKey(thread.ThreadID), strconv.FormatInt(message.MessageID, 10))
		return ThreadMessageResult{}, err
	}
	if awakened {
		thread.Status = dalmodel.ThreadStatusReady
		thread.ReadyUntil = now
		thread.Metadata = metadata
		return ThreadMessageResult{Thread: thread, Message: message}, nil
	}
	rows, err := c.threads.Get(ctx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}, Primary: true})
	if err != nil {
		_, _ = c.redis.ZRem(ctx, RedisPendingInputKey(thread.ThreadID), strconv.FormatInt(message.MessageID, 10))
		return ThreadMessageResult{}, err
	}
	if len(rows) == 0 {
		_, _ = c.redis.ZRem(ctx, RedisPendingInputKey(thread.ThreadID), strconv.FormatInt(message.MessageID, 10))
		return ThreadMessageResult{}, ErrThreadNotFound
	}
	thread = rows[0]
	switch thread.Status {
	case dalmodel.ThreadStatusReady:
		_, _ = c.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}, Statuses: []string{dalmodel.ThreadStatusReady}, ReadyUntilAfter: &now}, map[string]any{"ready_until": now})
	case dalmodel.ThreadStatusRunning, dalmodel.ThreadStatusBlocked:
	case dalmodel.ThreadStatusClosing, dalmodel.ThreadStatusClosed:
		_, _ = c.redis.ZRem(ctx, RedisPendingInputKey(thread.ThreadID), strconv.FormatInt(message.MessageID, 10))
		return ThreadMessageResult{}, ErrThreadClosed
	default:
		_, _ = c.redis.ZRem(ctx, RedisPendingInputKey(thread.ThreadID), strconv.FormatInt(message.MessageID, 10))
		return ThreadMessageResult{}, fmt.Errorf("wake thread %d conflict: unexpected status %q", thread.ThreadID, thread.Status)
	}
	return ThreadMessageResult{Thread: thread, Message: message}, nil
}

// Acquire 拉取当前线程的新输入，或扫描领取一个可运行线程。
func (c *Manager) Acquire(ctx context.Context, req AcquireRequest) (result AcquireResult, err error) {
	now := time.Now()

	if req.ThreadID != 0 {
		if req.LeaseToken == "" {
			return AcquireResult{}, ErrLeaseMismatch
		}
		rows, err := c.threads.Get(ctx, &dalmodel.ThreadFilter{IDs: []int64{req.ThreadID}, Primary: true})
		if err != nil {
			return AcquireResult{}, err
		}
		if len(rows) == 0 {
			return AcquireResult{}, daldb.MySQLErrRecordNotFound
		}
		thread := rows[0]
		if thread.LeaseToken != req.LeaseToken || !thread.ReadyUntil.After(now) ||
			(thread.Status != dalmodel.ThreadStatusRunning && thread.Status != dalmodel.ThreadStatusClosing) {
			return AcquireResult{}, ErrLeaseMismatch
		}
		messages, err := c.readQueuedMessages(ctx, req.ThreadID, false)
		return AcquireResult{PendingMessages: messages, ServerTimeMS: now.UnixMilli()}, err
	}

	err = c.db.Transaction(ctx, func(txCtx context.Context) (txErr error) {
		rows, err := c.threads.Get(txCtx, &dalmodel.ThreadFilter{RunnableUntil: &now, Primary: true, ForUpdate: true, Limit: 1})
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return ErrThreadNotRunnable
		}

		thread := rows[0]
		lease := &Lease{ThreadID: thread.ThreadID, LeaseToken: uuid.NewString(), LeaseUntil: now.Add(normalizeLeaseDuration(req.LeaseMS))}
		values := map[string]any{"lease_token": lease.LeaseToken, "ready_until": lease.LeaseUntil}
		if thread.Status == dalmodel.ThreadStatusReady {
			values["status"] = dalmodel.ThreadStatusRunning
		}
		if _, err = c.threads.Update(txCtx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}}, values); err != nil {
			return err
		}
		if thread.Status == dalmodel.ThreadStatusRunning {
			if err = c.recoverAcceptedInputs(txCtx, thread); err != nil {
				return err
			}
		}
		messages, err := c.readQueuedMessages(txCtx, thread.ThreadID, false)
		if err != nil {
			return err
		}

		thread.LeaseToken = lease.LeaseToken
		thread.ReadyUntil = lease.LeaseUntil
		if thread.Status == dalmodel.ThreadStatusReady {
			thread.Status = dalmodel.ThreadStatusRunning
		}
		result = AcquireResult{Thread: thread, Lease: lease, PendingMessages: messages, ServerTimeMS: now.UnixMilli()}
		return nil
	})
	if errors.Is(err, ErrThreadNotRunnable) {
		return AcquireResult{}, nil
	}
	return result, err
}

func (c *Manager) Renew(ctx context.Context, threadID int64, leaseToken string, leaseMS int64) (lease *Lease, err error) {
	now := time.Now()
	lease = &Lease{
		ThreadID:   threadID,
		LeaseToken: leaseToken,
		LeaseUntil: now.Add(normalizeLeaseDuration(leaseMS)),
	}
	changed, err := c.threads.Update(ctx, &dalmodel.ThreadFilter{

		IDs:          []int64{threadID},
		Statuses:     []string{dalmodel.ThreadStatusRunning, dalmodel.ThreadStatusClosing},
		LeaseTokens:  []string{leaseToken},
		LeaseValidAt: &now,
	},
		map[string]any{
			"ready_until": lease.LeaseUntil,
		})
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, ErrLeaseMismatch
	}
	return lease, nil
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
	return result, err
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

func (c *Manager) recoverAcceptedInputs(ctx context.Context, thread *dalmodel.Thread) (err error) {
	accepted, err := c.readQueuedMessages(ctx, thread.ThreadID, true)
	if err != nil || len(accepted) == 0 {
		return err
	}

	acceptedKey := RedisAcceptedInputKey(thread.ThreadID)
	pendingKey := RedisPendingInputKey(thread.ThreadID)
	for _, message := range accepted {
		member := strconv.FormatInt(message.MessageID, 10)
		if message.IsControl() ||
			message.Status == dalmodel.MessageStatusCanceled ||
			message.Status == dalmodel.MessageStatusCompleted ||
			message.Status == dalmodel.MessageStatusInterrupted {
			_, err = c.redis.ZRem(ctx, acceptedKey, member)
			if err != nil {
				return err
			}
			continue
		}

		_, err = c.messages.Update(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{thread.ThreadID}, IDs: []int64{message.MessageID}}, map[string]any{"status": dalmodel.MessageStatusPending})
		if err != nil {
			return err
		}
		_, err = c.redis.ZAdd(ctx, pendingKey, []redispkg.Z{{Score: float64(message.MessageID), Member: member}})
		if err != nil {
			return err
		}
		_, err = c.redis.ZRem(ctx, acceptedKey, member)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Manager) Resume(ctx context.Context, threadID int64, resumeMessageInput *InputMessage) (result ThreadMessageResult, err error) {
	rows, err := c.threads.Get(ctx, &dalmodel.ThreadFilter{IDs: []int64{threadID}, Primary: true})
	if err != nil {
		return result, err
	}
	if len(rows) == 0 {
		return result, ErrThreadNotFound
	}
	thread := rows[0]
	if thread.Status != dalmodel.ThreadStatusBlocked {
		return result, ErrThreadNotBlocked
	}

	now := time.Now()
	metadata := maps.Clone(thread.Metadata)
	if metadata == nil {
		metadata = map[string]string{}
	}

	var resumeMessage *dalmodel.Message
	if resumeMessageInput != nil {
		messageID, err := IDNextSharedID(ctx, c.redis)
		if err != nil {
			return result, err
		}
		messageMetadata := maps.Clone(resumeMessageInput.Metadata)
		if messageMetadata == nil {
			messageMetadata = map[string]string{}
		}
		resumeMessage = &dalmodel.Message{
			MessageID:   messageID,
			ThreadID:    threadID,
			Sender:      &dalmodel.Sender{Type: dalmodel.RecordNormalizeSenderType(resumeMessageInput.SenderType), ID: resumeMessageInput.SenderID},
			MessageType: resumeMessageInput.MessageType,
			Status:      dalmodel.MessageStatusPending,
			Payload:     []byte(string(resumeMessageInput.Payload)),
			Metadata:    messageMetadata,
			CreatedAt:   now,
		}
		if err = c.messages.Create(ctx, resumeMessage); err != nil {
			return result, err
		}
		if c.redis == nil {
			return result, ErrRedisUnavailable
		}
		member := strconv.FormatInt(resumeMessage.MessageID, 10)
		_, err = c.redis.ZAdd(ctx, RedisPendingInputKey(resumeMessage.ThreadID), []redispkg.Z{{Score: -float64(resumeMessage.MessageID), Member: member}})
		if err != nil {
			return result, err
		}
	}

	status := dalmodel.ThreadStatusReady
	var readyAt any = now
	if resumeMessage == nil {
		pending, err := c.readQueuedMessages(ctx, threadID, false)
		if err != nil {
			return result, err
		}
		if len(pending) == 0 {
			status = dalmodel.ThreadStatusIdle
			readyAt = nil
		}
	}

	changed, err := c.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{threadID}, Statuses: []string{dalmodel.ThreadStatusBlocked}}, map[string]any{
		"status": status, "ready_until": readyAt, "metadata_json": metadata, "lease_token": "",
	})
	if err != nil {
		if resumeMessage != nil && c.redis != nil {
			_, _ = c.redis.ZRem(ctx, RedisPendingInputKey(threadID), strconv.FormatInt(resumeMessage.MessageID, 10))
		}
		return result, err
	}
	if !changed {
		return result, ErrThreadNotBlocked
	}

	rows, err = c.threads.Get(ctx, &dalmodel.ThreadFilter{IDs: []int64{threadID}, Primary: true})
	if err != nil {
		return result, err
	}
	if len(rows) == 0 {
		return result, daldb.MySQLErrRecordNotFound
	}
	return ThreadMessageResult{Thread: rows[0], Message: resumeMessage}, nil
}

func (c *Manager) ReleaseThread(ctx context.Context, threadID int64, leaseToken, reason string, status dalmodel.ThreadStatus) (thread *dalmodel.Thread, err error) {
	if status != "" && status != dalmodel.ThreadStatusBlocked {
		return nil, ErrInvalidStatusTransition
	}

	nextStatus := status
	if nextStatus == "" {
		nextStatus = dalmodel.ThreadStatusIdle
		pending, err := c.readQueuedMessages(ctx, threadID, false)
		if err != nil {
			return nil, err
		}
		if len(pending) > 0 {
			nextStatus = dalmodel.ThreadStatusReady
		}
	}

	now := time.Now()
	readyUntil := now
	if _, failed := defaultFailureReleaseReasons[strings.ToLower(strings.TrimSpace(reason))]; failed {
		readyUntil = now.Add(defaultFailureReleaseBackoff)
	}

	filter := &dalmodel.ThreadFilter{IDs: []int64{threadID}, Statuses: []string{dalmodel.ThreadStatusRunning}, LeaseTokens: []string{leaseToken}, LeaseValidAt: &now}
	values := map[string]any{"status": nextStatus, "ready_until": nil, "lease_token": ""}
	if nextStatus == dalmodel.ThreadStatusReady {
		values["ready_until"] = readyUntil
	}
	changed, err := c.threads.Update(ctx, filter, values)
	if err != nil {
		return nil, err
	}
	if !changed {
		filter.Statuses = []string{dalmodel.ThreadStatusClosing}
		changed, err = c.threads.Update(ctx, filter, map[string]any{"ready_until": readyUntil, "lease_token": ""})
		if err != nil {
			return nil, err
		}
		if !changed {
			return nil, ErrLeaseMismatch
		}
	}

	rows, err := c.threads.Get(ctx, &dalmodel.ThreadFilter{IDs: []int64{threadID}, Primary: true})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrThreadNotFound
	}
	thread = rows[0]
	if status != "" || thread.Status != dalmodel.ThreadStatusIdle {
		return thread, nil
	}

	pending, err := c.readQueuedMessages(ctx, threadID, false)
	if err != nil || len(pending) == 0 {
		return thread, err
	}
	readyAt := time.Now()
	changed, err = c.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{threadID}, Statuses: []string{dalmodel.ThreadStatusIdle}}, map[string]any{"status": dalmodel.ThreadStatusReady, "ready_until": readyAt})
	if err != nil || !changed {
		return thread, err
	}
	thread.Status = dalmodel.ThreadStatusReady
	thread.ReadyUntil = readyAt
	return thread, nil
}

func (c *Manager) AckInput(ctx context.Context, threadID int64, leaseToken, runID string, acceptedMessageIDs []int64) (delivered []*dalmodel.Message, err error) {
	if c.redis == nil {
		return nil, ErrRedisUnavailable
	}
	if len(acceptedMessageIDs) == 0 {
		return nil, nil
	}

	err = c.db.Transaction(ctx, func(txCtx context.Context) (txErr error) {
		rows, txErr := c.threads.Get(txCtx, &dalmodel.ThreadFilter{IDs: []int64{threadID}, Primary: true, ForUpdate: true})
		if txErr != nil {
			return txErr
		}
		if len(rows) == 0 {
			return ErrThreadNotFound
		}
		thread := rows[0]
		if thread.LeaseToken != leaseToken || !thread.ReadyUntil.After(time.Now()) || (thread.Status != dalmodel.ThreadStatusRunning && thread.Status != dalmodel.ThreadStatusClosing) {
			return ErrLeaseMismatch
		}

		messages, txErr := c.messages.Get(txCtx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, IDs: acceptedMessageIDs, Primary: true})
		if txErr != nil {
			return txErr
		}
		byID := make(map[int64]*dalmodel.Message, len(messages))
		for _, message := range messages {
			byID[message.MessageID] = message
		}

		members := make([]any, 0, len(acceptedMessageIDs))
		accepted := make([]redispkg.Z, 0, len(acceptedMessageIDs))
		for _, id := range acceptedMessageIDs {
			message := byID[id]
			if message == nil {
				return fmt.Errorf("%w: message_id=%d", InputErrMessageNotFound, id)
			}
			if message.Status == dalmodel.MessageStatusPending && runID != "" {
				message.TriggerRunID = runID
			}
			delivered = append(delivered, message)
			member := strconv.FormatInt(id, 10)
			members = append(members, member)
			accepted = append(accepted, redispkg.Z{Score: float64(id), Member: member})
		}

		values := map[string]any{"status": dalmodel.MessageStatusAcked}
		if runID != "" {
			values["trigger_turn_id"] = runID
		}
		_, txErr = c.messages.Update(txCtx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, IDs: acceptedMessageIDs, Statuses: []string{dalmodel.MessageStatusPending, dalmodel.MessageStatusAcked}}, values)
		if txErr != nil {
			return txErr
		}
		if _, txErr = c.redis.ZAdd(txCtx, RedisAcceptedInputKey(threadID), accepted); txErr != nil {
			return txErr
		}
		_, txErr = c.redis.ZRem(txCtx, RedisPendingInputKey(threadID), members...)
		return txErr
	})
	return delivered, err
}

func (c *Manager) cancelQueuedInputsUntil(ctx context.Context, threadID int64, cancelUntilMessageID int64) (err error) {
	if c.redis == nil {
		return ErrRedisUnavailable
	}
	members, err := c.redis.ZRange(ctx, RedisPendingInputKey(threadID), 0, -1)
	if err != nil || len(members) == 0 {
		return err
	}

	ids := make([]int64, 0, len(members))
	for _, member := range members {
		id, parseErr := strconv.ParseInt(member, 10, 64)
		if parseErr != nil {
			return parseErr
		}
		if id <= cancelUntilMessageID {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	messages, err := c.messages.Get(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, IDs: ids, Statuses: []string{dalmodel.MessageStatusPending}, Primary: true})
	if err != nil {
		return err
	}
	ids = ids[:0]
	membersToRemove := make([]any, 0, len(messages))
	for _, message := range messages {
		if !message.IsControl() {
			ids = append(ids, message.MessageID)
			membersToRemove = append(membersToRemove, strconv.FormatInt(message.MessageID, 10))
		}
	}
	if len(ids) == 0 {
		return nil
	}
	if _, err = c.messages.Update(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, IDs: ids, Statuses: []string{dalmodel.MessageStatusPending}}, map[string]any{"status": dalmodel.MessageStatusCanceled}); err != nil {
		return err
	}
	_, err = c.redis.ZRem(ctx, RedisPendingInputKey(threadID), membersToRemove...)
	return err
}

func (c *Manager) enqueueControlMessage(ctx context.Context, message *dalmodel.Message) (err error) {
	if c.redis == nil {
		return ErrRedisUnavailable
	}
	if err = c.messages.Create(ctx, message); err != nil {
		return err
	}
	member := strconv.FormatInt(message.MessageID, 10)
	_, err = c.redis.ZAdd(ctx, RedisPendingInputKey(message.ThreadID), []redispkg.Z{{Score: -float64(message.MessageID), Member: member}})
	return err
}

func (c *Manager) Cancel(ctx context.Context, threadID int64, reason string, cutoffMessageID *int64) (result *ThreadMessageResult, err error) {
	rows, err := c.threads.Get(ctx, &dalmodel.ThreadFilter{IDs: []int64{threadID}, Primary: true})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrThreadNotFound
	}
	thread := rows[0]
	if thread.Status == dalmodel.ThreadStatusClosing || thread.Status == dalmodel.ThreadStatusClosed {
		return nil, ErrThreadClosed
	}
	if thread.Status == dalmodel.ThreadStatusBlocked {
		return nil, ErrThreadBlocked
	}
	if reason == "" {
		reason = DefaultCancelInputReason
	}

	var cancelUntilMessageID int64
	if cutoffMessageID != nil {
		cancelUntilMessageID = *cutoffMessageID
		if cancelUntilMessageID <= 0 {
			return nil, fmt.Errorf("%w: cutoff_message_id must be positive", ErrInvalidCancel)
		}
		message, err := c.findMessage(ctx, threadID, cancelUntilMessageID)
		if err != nil {
			return nil, fmt.Errorf("%w: cutoff_message_id=%d", ErrInvalidCancel, cancelUntilMessageID)
		}
		if message.IsControl() {
			return nil, fmt.Errorf("%w: cutoff_message_id=%d is control message", ErrInvalidCancel, cancelUntilMessageID)
		}
	} else {
		messages, err := c.messages.Get(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, InputOnly: true, Primary: true, Desc: true, Limit: 1})
		if err != nil {
			return nil, err
		}
		if len(messages) > 0 {
			cancelUntilMessageID = messages[0].MessageID
		}
	}
	if cancelUntilMessageID == 0 {
		return &ThreadMessageResult{Thread: thread}, nil
	}
	if err = c.cancelQueuedInputsUntil(ctx, threadID, cancelUntilMessageID); err != nil {
		return nil, err
	}

	messageID, err := IDNextSharedID(ctx, c.redis)
	if err != nil {
		return nil, err
	}
	requestID := strconv.FormatInt(messageID, 10)
	metadata := map[string]string{"control_type": dalmodel.ControlTypeCancelInput, "request_id": requestID, "cutoff_message_id": strconv.FormatInt(cancelUntilMessageID, 10)}
	if reason != "" {
		metadata["reason"] = reason
	}
	payload, err := json.Marshal(CancelInputControlPayload{ControlType: dalmodel.ControlTypeCancelInput, RequestID: requestID, ThreadID: threadID, CutoffMessageID: cancelUntilMessageID, Reason: reason})
	if err != nil {
		return nil, err
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
	if err = c.enqueueControlMessage(ctx, controlMessage); err != nil {
		return nil, err
	}

	if thread.Status == dalmodel.ThreadStatusIdle {
		now := time.Now()
		changed, err := c.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}, Statuses: []string{dalmodel.ThreadStatusIdle}}, map[string]any{"status": dalmodel.ThreadStatusReady, "ready_until": now})
		if err != nil {
			return nil, err
		}
		if changed {
			thread.Status = dalmodel.ThreadStatusReady
			thread.ReadyUntil = now
		}
	}
	return &ThreadMessageResult{Thread: thread, Message: controlMessage}, nil
}

func (c *Manager) Close(ctx context.Context, threadID int64, reason string) (result *ThreadMessageResult, err error) {
	rows, err := c.threads.Get(ctx, &dalmodel.ThreadFilter{IDs: []int64{threadID}, Primary: true})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrThreadNotFound
	}
	thread := rows[0]
	if reason == "" {
		reason = DefaultCloseThreadReason
	}

	switch thread.Status {
	case dalmodel.ThreadStatusClosed:
		return &ThreadMessageResult{Thread: thread}, nil
	case dalmodel.ThreadStatusIdle, dalmodel.ThreadStatusReady, dalmodel.ThreadStatusBlocked:
		changed, err := c.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{threadID}, Statuses: []string{thread.Status}}, map[string]any{"status": dalmodel.ThreadStatusClosed, "ready_until": nil, "lease_token": ""})
		if err != nil {
			return nil, err
		}
		if !changed {
			return nil, ErrInvalidClose
		}
		thread.Status = dalmodel.ThreadStatusClosed
		thread.ReadyUntil = time.Time{}
		thread.LeaseToken = ""
		if c.redis != nil {
			_ = c.cancelQueuedInputsUntil(ctx, threadID, int64(1<<63-1))
		}
		return &ThreadMessageResult{Thread: thread}, nil
	case dalmodel.ThreadStatusRunning, dalmodel.ThreadStatusClosing:
	default:
		return nil, ErrInvalidClose
	}

	if c.redis == nil {
		return nil, ErrRedisUnavailable
	}
	if thread.Status == dalmodel.ThreadStatusRunning {
		now := time.Now()
		readyUntil := now
		if thread.ReadyUntil.After(now) {
			readyUntil = thread.ReadyUntil
		}
		changed, err := c.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{threadID}, Statuses: []string{dalmodel.ThreadStatusRunning}}, map[string]any{"status": dalmodel.ThreadStatusClosing, "ready_until": readyUntil})
		if err != nil {
			return nil, err
		}
		if !changed {
			return nil, ErrInvalidClose
		}
		thread.Status = dalmodel.ThreadStatusClosing
		thread.ReadyUntil = readyUntil
	}
	if err = c.cancelQueuedInputsUntil(ctx, threadID, int64(1<<63-1)); err != nil {
		return nil, err
	}

	pending, err := c.readQueuedMessages(ctx, threadID, false)
	if err != nil {
		return nil, err
	}
	for _, message := range pending {
		if message.Status == dalmodel.MessageStatusPending && message.IsCloseControl() {
			return &ThreadMessageResult{Thread: thread, Message: message}, nil
		}
	}

	messageID, err := IDNextSharedID(ctx, c.redis)
	if err != nil {
		return nil, err
	}
	requestID := strconv.FormatInt(messageID, 10)
	metadata := map[string]string{"control_type": dalmodel.ControlTypeCloseThread, "request_id": requestID}
	if reason != "" {
		metadata["reason"] = reason
	}
	payload, err := json.Marshal(CloseThreadControlPayload{ControlType: dalmodel.ControlTypeCloseThread, RequestID: requestID, ThreadID: threadID, Reason: reason})
	if err != nil {
		return nil, err
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
	if err = c.enqueueControlMessage(ctx, controlMessage); err != nil {
		return nil, err
	}
	return &ThreadMessageResult{Thread: thread, Message: controlMessage}, nil
}

func (c *Manager) ConfirmThreadClosed(ctx context.Context, threadID int64, leaseToken string, controlMessageID int64) (result *ThreadMessageResult, err error) {
	if controlMessageID <= 0 {
		return nil, fmt.Errorf("%w: control_message_id must be positive", ErrInvalidClose)
	}

	rows, err := c.threads.Get(ctx, &dalmodel.ThreadFilter{IDs: []int64{threadID}, Primary: true})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, ErrThreadNotFound
	}
	thread := rows[0]

	controlMessage, err := c.findMessage(ctx, threadID, controlMessageID)
	if err != nil {
		return nil, fmt.Errorf("%w: control_message_id=%d", ErrInvalidClose, controlMessageID)
	}
	if !controlMessage.IsCloseControl() {
		return nil, fmt.Errorf("%w: control_message_id=%d is not close control", ErrInvalidClose, controlMessageID)
	}

	if thread.Status != dalmodel.ThreadStatusClosed {
		if thread.Status != dalmodel.ThreadStatusClosing {
			return nil, ErrInvalidClose
		}
		now := time.Now()
		if thread.LeaseToken != leaseToken || thread.ReadyUntil.IsZero() || thread.ReadyUntil.Before(now) {
			return nil, ErrLeaseMismatch
		}
		changed, err := c.threads.Update(ctx, &dalmodel.ThreadFilter{
			IDs:          []int64{threadID},
			Statuses:     []string{dalmodel.ThreadStatusClosing},
			LeaseTokens:  []string{leaseToken},
			LeaseValidAt: &now,
		}, map[string]any{"status": dalmodel.ThreadStatusClosed, "ready_until": nil, "lease_token": ""})
		if err != nil {
			return nil, err
		}
		if !changed {
			return nil, ErrLeaseMismatch
		}
		thread.Status = dalmodel.ThreadStatusClosed
		thread.ReadyUntil = time.Time{}
		thread.LeaseToken = ""
	}

	_, _ = c.messages.Update(ctx, &dalmodel.MessageFilter{
		ThreadIDs: []int64{threadID}, IDs: []int64{controlMessageID}, Statuses: []string{dalmodel.MessageStatusPending},
	}, map[string]any{"status": dalmodel.MessageStatusAcked})
	if c.redis != nil {
		_, _ = c.redis.ZRem(ctx, RedisPendingInputKey(threadID), strconv.FormatInt(controlMessageID, 10))
	}
	return &ThreadMessageResult{Thread: thread, Message: controlMessage}, nil
}

type outputPayload struct {
	Status             string   `json:"status,omitempty"`
	Kind               string   `json:"kind,omitempty"`
	ConsumedMessageIDs []string `json:"consumed_message_ids"`
	LLMResponseID      string   `json:"llm_response_id"`
	ToolCallID         string   `json:"tool_call_id"`
	InterruptID        string   `json:"interrupt_id"`
	OutputDelta        *string  `json:"output_delta,omitempty"`
}

type outputEventAction int

type outputMessageKeySource int

const (
	outputActionLiveOnly outputEventAction = iota
	outputActionUpdateInput
	outputActionSaveMessage
)

const (
	messageKeyFromPayloadHash outputMessageKeySource = iota
	messageKeyFromLLMResponseID
	messageKeyFromToolCallID
	messageKeyFromInterruptID
	messageKeyLatestInRun
)

type outputEventRule struct {
	action           outputEventAction
	messageType      string
	sender           string
	messageKeySource outputMessageKeySource
	messageStatus    string
}

var outputEventRules = map[string]outputEventRule{
	eventpkg.EventTypeRunStatus.String():        {action: outputActionUpdateInput},
	eventpkg.EventTypeAssistantMessage.String(): {action: outputActionSaveMessage, messageType: "assistant", sender: dalmodel.SenderTypeAgent, messageKeySource: messageKeyFromLLMResponseID},
	eventpkg.EventTypeToolCall.String():         {action: outputActionSaveMessage, messageType: "tool", sender: dalmodel.SenderTypeAgent, messageKeySource: messageKeyFromToolCallID},
	eventpkg.EventTypeInputRequired.String():    {action: outputActionSaveMessage, sender: dalmodel.SenderTypeSystem, messageKeySource: messageKeyFromInterruptID},
	eventpkg.EventTypePlanUpdated.String():      {action: outputActionSaveMessage, messageType: "plan", sender: dalmodel.SenderTypeSystem, messageKeySource: messageKeyLatestInRun},
	eventpkg.EventTypeError.String():            {action: outputActionSaveMessage, messageType: "error", sender: dalmodel.SenderTypeSystem, messageKeySource: messageKeyFromPayloadHash},
	eventpkg.EventTypeAssistantDelta.String():   {action: outputActionLiveOnly},
}

// saveOutput 保存模型输出并向客户端投递实时事件。
// 入参：ctx 为调用上下文；request 包含线程 ID、许可、RunID 和输出事件。
// 出参：err 表示许可、事件格式、数据库、Redis 或实时通道错误。
// 主要逻辑：事务内校验许可；按规则表更新输入状态或保存历史消息；事务外推送实时事件。
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
		if leaseToken == "" || owner.LeaseToken != leaseToken || !owner.ReadyUntil.After(time.Now()) || (owner.Status != dalmodel.ThreadStatusRunning && owner.Status != dalmodel.ThreadStatusClosing) {
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
			rule := outputEventRuleFor(output.EventType, payload)
			switch rule.action {
			case outputActionUpdateInput:
				txErr = c.updateInputExecution(txCtx, threadID, output, payload, rule)
			case outputActionSaveMessage:
				txErr = c.saveOutputMessage(txCtx, threadID, output, payload, originalID, rule)
			}
			if txErr != nil {
				return txErr
			}
		}
		if !owner.ReadyUntil.After(time.Now()) {
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

func outputEventRuleFor(eventType string, payload outputPayload) outputEventRule {
	rule, ok := outputEventRules[eventType]
	if !ok {
		return outputEventRule{action: outputActionLiveOnly}
	}
	if eventType == eventpkg.EventTypeRunStatus.String() {
		switch payload.Status {
		case eventpkg.RunStatusFinished:
			rule.messageStatus = dalmodel.MessageStatusCompleted
		case eventpkg.RunStatusInterrupted:
			rule.messageStatus = dalmodel.MessageStatusInterrupted
		}
	}
	if eventType == eventpkg.EventTypeToolCall.String() && payload.OutputDelta != nil {
		rule.action = outputActionLiveOnly
	}
	if eventType == eventpkg.EventTypeInputRequired.String() {
		switch payload.Kind {
		case eventpkg.InputRequiredKindApproval:
			rule.messageType = "approval"
		case eventpkg.InputRequiredKindPlanInput:
			rule.messageType = "question"
		default:
			rule.messageType = "interrupt"
		}
	}
	return rule
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

func parseOutputPayload(raw []byte) (payload outputPayload, err error) {
	err = json.Unmarshal(raw, &payload)
	return payload, err
}

func (c *Manager) updateInputExecution(ctx context.Context, threadID int64, output *OutputFrame, payload outputPayload, rule outputEventRule) (err error) {
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
	values := map[string]any{"trigger_turn_id": output.RunID}
	if rule.messageStatus != "" {
		values["status"] = rule.messageStatus
	}
	_, err = c.messages.Update(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, IDs: ids}, values)
	return err
}

func outputMessageKey(output *OutputFrame, payload outputPayload, originalID int64, rule outputEventRule) string {
	key := ""
	switch rule.messageKeySource {
	case messageKeyFromLLMResponseID:
		key = payload.LLMResponseID
	case messageKeyFromToolCallID:
		key = payload.ToolCallID
	case messageKeyFromInterruptID:
		key = payload.InterruptID
	case messageKeyLatestInRun:
		key = "latest"
	}
	if key == "" && originalID != 0 {
		key = strconv.FormatInt(originalID, 10)
	}
	if key == "" {
		sum := sha256.Sum256(output.Payload)
		key = fmt.Sprintf("%x", sum)
	}
	return output.RunID + ":" + rule.messageType + ":" + key
}

func (c *Manager) saveOutputMessage(ctx context.Context, threadID int64, output *OutputFrame, payload outputPayload, originalID int64, rule outputEventRule) (err error) {
	key := outputMessageKey(output, payload, originalID, rule)
	if rule.messageType == "tool" {
		if err = c.mergeToolOutput(ctx, threadID, key, output); err != nil {
			return err
		}
	}
	return c.messages.Create(ctx, &dalmodel.Message{
		MessageID: output.EventID, ThreadID: threadID, Sender: &dalmodel.Sender{Type: rule.sender, ID: "model"},
		MessageType: rule.messageType, Status: dalmodel.MessageStatusAcked, Payload: output.Payload,
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
	if err = json.Unmarshal(previous[0].Payload, &before); err != nil {
		return err
	}
	if err = json.Unmarshal(output.Payload, &after); err != nil {
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

// readQueuedMessages 按 Redis 队列顺序读取消息，并清理 pending 队列中的终态残留。
func (s *Manager) readQueuedMessages(ctx context.Context, threadID int64, accepted bool) (messages []*dalmodel.Message, err error) {
	if s.redis == nil {
		return nil, ErrRedisUnavailable
	}
	key := RedisPendingInputKey(threadID)
	if accepted {
		key = RedisAcceptedInputKey(threadID)
	}
	members, err := s.redis.ZRange(ctx, key, 0, -1)
	if err != nil || len(members) == 0 {
		return nil, err
	}
	ids := make([]int64, 0, len(members))
	for _, member := range members {
		id, err := strconv.ParseInt(member, 10, 64)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}

	rows, err := s.messages.Get(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{threadID}, IDs: ids, Primary: true})
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*dalmodel.Message, len(rows))
	for _, msg := range rows {
		byID[msg.MessageID] = msg
	}

	residues := make([]any, 0)
	for _, id := range ids {
		msg := byID[id]
		if msg == nil {
			return nil, fmt.Errorf("%w: message_id=%d", InputErrMessageNotFound, id)
		}
		if !accepted && msg.Status != dalmodel.MessageStatusPending {
			residues = append(residues, strconv.FormatInt(id, 10))
			continue
		}
		if accepted || len(messages) < queuedMessageLimit {
			messages = append(messages, msg)
		}
	}
	if len(residues) > 0 {
		_, err = s.redis.ZRem(ctx, key, residues...)
	}
	return messages, err
}
