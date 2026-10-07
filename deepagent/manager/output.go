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
	"eino-cli/deepagent/dal/model"
	eventpkg "eino-cli/deepagent/protocol/event"

	redis "github.com/redis/go-redis/v9"
)

type OutputFrame struct {
	EventID   int64             `json:"event_id"`
	QueueID   string            `json:"queue_id,omitempty"`
	ThreadID  int64             `json:"thread_id"`
	SessionID string            `json:"session_id"`
	RunID     string            `json:"run_id"`
	EventType string            `json:"event_type"`
	Payload   []byte            `json:"payload"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

func (c *Manager) SaveOutput(ctx context.Context, threadID int64, leaseToken, runID string, frames []OutputFrame) (err error) {
	if len(frames) == 0 {
		return nil
	}
	outputs := cloneEvents(frames)
	err = c.db.Transaction(ctx, func(txCtx context.Context) (txErr error) {
		threads, txErr := c.threads.Get(txCtx, &model.ThreadFilter{IDs: []int64{threadID}, Primary: true, ForUpdate: true})
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
	output.EventID, err = cache.GenerateID(ctx, c.redis)
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
	values := map[string]any{"trigger_turn_id": output.RunID, "status": model.MessageStatusAccepted}
	_, err = c.messages.Update(ctx, &model.MessageFilter{ThreadIDs: []int64{threadID}, IDs: ids, Statuses: []string{model.MessageStatusPending, model.MessageStatusAccepted}}, values)
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
	return c.messages.Create(ctx, &model.Message{
		MessageID: output.EventID, ThreadID: threadID, Sender: &model.Sender{Type: rule.sender, ID: "model"},
		MessageType: rule.messageType, Payload: output.Payload,
		Metadata: output.Metadata, TriggerRunID: output.RunID, CreatedAt: output.CreatedAt, OutputKey: &key,
	})
}

func (c *Manager) mergeToolOutput(ctx context.Context, threadID int64, key string, output *OutputFrame) (err error) {
	previous, err := c.messages.Get(ctx, &model.MessageFilter{Primary: true, Take: true, SkipNormalize: true, ThreadIDs: []int64{threadID}, OutputKey: &key})
	if errors.Is(err, db.MySQLErrRecordNotFound) {
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

// saveRunStatus executes under the Thread row lock, alongside consumed-input binding.
func (c *Manager) saveRunStatus(ctx context.Context, thread *model.Thread, output *OutputFrame, payload outputPayload) error {
	if output.EventType != eventpkg.EventTypeRunStatus.String() {
		return nil
	}
	status := payload.Status
	switch status {
	case eventpkg.RunStatusCompactStarted:
		// Automatic compaction belongs to the active Run; manual compaction starts one.
		if thread.LastRunID == output.RunID {
			return nil
		}
		status = eventpkg.RunStatusStarted
	case eventpkg.RunStatusStarted, eventpkg.RunStatusFinished, eventpkg.RunStatusInterrupted, eventpkg.RunStatusFailed:
	case eventpkg.RunStatusBlocked:
		if payload.CheckpointID == "" || payload.InterruptID == "" {
			return errors.New("blocked run lacks checkpoint or interrupt ID")
		}
	default:
		return nil
	}
	current := thread.LastRun
	if current != nil && !current.Ended() && current.LeaseToken == thread.LeaseToken && current.RunID != output.RunID {
		return fmt.Errorf("run outcome mismatch: current=%q received=%q", current.RunID, output.RunID)
	}
	runs, err := c.runs.Get(ctx, thread.ThreadID, []string{output.RunID})
	if err != nil {
		return err
	}
	run := runs[output.RunID]
	if run == nil {
		run = &model.RunRecord{RunID: output.RunID, ThreadID: thread.ThreadID}
	}
	// Duplicate terminal delivery cannot reopen a completed execution.
	if run.Ended() {
		if run.Status == status {
			return nil
		}
		return errors.New("Run has already ended")
	}
	run.Status, run.LeaseToken = status, thread.LeaseToken
	if status == eventpkg.RunStatusBlocked {
		run.CheckpointID, run.InterruptID = payload.CheckpointID, payload.InterruptID
	}
	err = c.runs.Save(ctx, run)
	if err != nil {
		return err
	}
	_, err = c.threads.Update(ctx, &model.ThreadFilter{IDs: []int64{thread.ThreadID}}, map[string]any{"last_run_id": run.RunID})
	if err != nil {
		return err
	}
	thread.LastRunID, thread.LastRun = run.RunID, run
	return nil
}

// SubscribeSession 建立 Session 实时订阅。
func (c *Manager) SubscribeSession(ctx context.Context, sessionID, recoverQueueID string) (subscription *Subscription, err error) {
	if c == nil || c.stream == nil {
		return nil, errors.New("manager stream is unavailable")
	}
	subscription = newSubscription(ctx, c.stream, SubscribeSessionRequest{SessionID: sessionID, RecoverQueueID: recoverQueueID}, c.subscribeSessionMaxIdle)
	return subscription, subscription.Err
}

type StreamStreamOut struct{ redis cache.RedisClient }

type SubscribeSessionRequest struct {
	SessionID      string
	RecoverQueueID string
}

type Subscription struct {
	Events <-chan OutputFrame
	Err    error
	Close  func() error
}

type streamEnvelope struct {
	Seq   int64       `json:"seq"`
	Frame OutputFrame `json:"frame"`
}

func (s *StreamStreamOut) FanoutEventRecords(ctx context.Context, sessionID string, frames []OutputFrame) error {
	for _, frame := range frames {
		seq, err := cache.GenerateSequence(ctx, s.redis, sessionEventKey(sessionID)+":seq")
		if err != nil {
			return err
		}
		frame.QueueID = strconv.FormatInt(seq, 10)
		envelope, err := json.Marshal(streamEnvelope{Seq: seq, Frame: frame})
		if err != nil {
			return err
		}
		key := fmt.Sprintf("%s:%d", sessionEventKey(sessionID), seq)
		err = s.redis.SetRaw(ctx, key, envelope, 24*time.Hour)
		if err != nil {
			return err
		}
		_, err = s.redis.ZAdd(ctx, sessionEventKey(sessionID), []redis.Z{{Score: float64(seq), Member: strconv.FormatInt(seq, 10)}})
		if err != nil {
			return err
		}
		err = s.redis.Publish(ctx, sessionChannel(sessionID), envelope)
		if err != nil {
			return err
		}
	}
	return nil
}
