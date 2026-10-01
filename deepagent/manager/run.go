package manager

import (
	"context"
	inputpkg "eino-cli/deepagent/protocol/input"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"eino-cli/deepagent/dal/model"
	eventpkg "eino-cli/deepagent/protocol/event"
)

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

func (c *Manager) createInput(ctx context.Context, threadID int64, input *InputMessage) (*model.Message, error) {
	id, err := IDNextSharedID(ctx, c.redis)
	if err != nil {
		return nil, err
	}
	message := &model.Message{
		MessageID: id, ThreadID: threadID, CreatedAt: time.Now(),
		Sender:      &model.Sender{Type: model.RecordNormalizeSenderType(input.SenderType), ID: input.SenderID},
		MessageType: input.MessageType, Status: model.MessageStatusPending,
		Payload: append([]byte(nil), input.Payload...), Metadata: input.Metadata,
	}
	if input.MessageType == inputpkg.MessageTypeResume {
		var resume inputpkg.ResumeRunPayload
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
func (c *Manager) ensureRun(ctx context.Context, thread *model.Thread, runID string) error {
	runs, err := c.runs.Get(ctx, thread.ThreadID, []string{runID})
	if err != nil {
		return err
	}
	run := runs[runID]
	if run == nil {
		run = &model.RunRecord{RunID: runID, ThreadID: thread.ThreadID, Status: eventpkg.RunStatusStarted, LeaseToken: thread.LeaseToken}
		err = c.runs.Save(ctx, run)
		if err != nil {
			return err
		}
	}
	if !run.Ended() && (thread.LastRun == nil || thread.LastRun.Ended()) {
		_, err = c.threads.Update(ctx, &model.ThreadFilter{IDs: []int64{thread.ThreadID}}, map[string]any{"last_run_id": runID})
		if err != nil {
			return err
		}
		thread.LastRunID, thread.LastRun = runID, run
	}
	return nil
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
