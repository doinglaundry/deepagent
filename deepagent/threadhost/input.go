//go:build !windows

package threadhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	dalmodel "eino-cli/deepagent/dal/model"
	"eino-cli/deepagent/helper/serialiser"
	"eino-cli/deepagent/manager"
	threadpkg "eino-cli/deepagent/thread"
)

// CancelInputControlPayload is the JSON payload for
// MessageTypeControlCancelInput.
type CancelInputControlPayload struct {
	CutoffMessageID int64  `json:"cutoff_message_id"`
	Reason          string `json:"reason,omitempty"`
}

// CloseThreadControlPayload is the JSON payload for
// MessageTypeControlCloseThread.
type CloseThreadControlPayload struct {
	Reason string `json:"reason,omitempty"`
}

func (c *threadRun) runInput(stop <-chan struct{}, activity chan<- time.Time, results chan<- runResult, done chan<- struct{}) {
	defer close(done)
	pending := c.claim.PendingMessages
	pollTicker := time.NewTicker(c.host.MessagePollInterval)
	defer pollTicker.Stop()
	consecutivePullErrors := 0

	for {
		for len(pending) > 0 {
			select {
			case <-c.ctx.Done():
				return
			case <-stop:
				return
			case <-c.acceptDone:
				return
			default:
			}
			message := pending[0]
			pending = pending[1:]
			if message == nil {
				continue
			}
			result := c.deliverMessage(message, &pending, stop)
			if !result.empty() {
				results <- result
				return
			}
			select {
			case activity <- time.Now():
			default:
			}
		}

		select {
		case <-c.ctx.Done():
			return
		case <-stop:
			return
		case <-c.acceptDone:
			return
		case <-pollTicker.C:
			lease := c.claim.Lease
			result, err := c.host.Client.Acquire(c.ctx, manager.AcquireRequest{ThreadID: lease.ThreadID, LeaseToken: lease.LeaseToken})
			err = serialiser.WrapError(fmt.Sprintf("PullPendingMessages thread_id=%d", lease.ThreadID), err)
			if err != nil {
				if c.ctx.Err() != nil {
					return
				}
				consecutivePullErrors++
				if c.waitInput(stop, getPullErrorBackoff(c.host.MessagePollInterval, consecutivePullErrors)) {
					return
				}
				continue
			}
			consecutivePullErrors = 0
			messages := result.PendingMessages
			if len(messages) == 0 {
				continue
			}

			pending = append(pending, messages...)
		}
	}
}

// deliverMessage 将一条队列消息交给控制逻辑或 Thread，再确认投递。
func (c *threadRun) deliverMessage(message *dalmodel.Message, pending *[]*dalmodel.Message, stop <-chan struct{}) (result runResult) {
	switch message.MessageType {
	case MessageTypeControlCancelInput:
		return c.handleCancel(message, pending, stop)
	case MessageTypeControlCloseThread:
		return c.handleClose(message, pending, stop)
	}

	postResult, err := c.thread.PostMessage(c.ctx, ProtocolToWorkerMessage(message))
	if err != nil {
		err = fmt.Errorf("Thread.PostMessage thread_id=%d message_id=%d: %w", c.claim.Thread.ThreadID, message.MessageID, err)
		if c.ctx.Err() != nil {
			return runResult{}
		}
		if errors.Is(err, threadpkg.TransportErrThreadClosed) {
			return runResult{reason: defaultThreadClosedReason}
		}
		return runResult{reason: postMessageFailedReason, err: err}
	}
	triggerRunID := ""
	if postResult != nil {
		triggerRunID = postResult.RunID
	}
	return c.ackMessage(message, triggerRunID)
}

func (c *threadRun) ackMessage(message *dalmodel.Message, triggerRunID string) (result runResult) {
	lease := c.claim.Lease
	_, err := c.host.Client.AckInput(c.ctx, lease.ThreadID, lease.LeaseToken, triggerRunID, []int64{message.MessageID})
	err = serialiser.WrapError(fmt.Sprintf("AckThreadMessages thread_id=%d message_id=%d", lease.ThreadID, message.MessageID), err)
	if err != nil {
		return runResult{reason: ackMessageFailedReason, err: err}
	}
	return runResult{}
}

func (c *threadRun) handleCancel(message *dalmodel.Message, pending *[]*dalmodel.Message, stop <-chan struct{}) (result runResult) {
	var payload CancelInputControlPayload
	decodeErr := json.Unmarshal(message.Payload, &payload)
	if decodeErr != nil || payload.CutoffMessageID <= 0 {
		if decodeErr == nil {
			decodeErr = fmt.Errorf("cancel input control missing cutoff_message_id")
		}
		*pending = nil
		result = c.ackMessage(message, "")
		if !result.empty() {
			return result
		}
		return runResult{reason: controlInputFailedReason, err: decodeErr}
	}

	*pending = dropCanceledMessages(*pending, payload.CutoffMessageID)
	if c.thread.ActiveRun() == nil {
		return c.ackMessage(message, "")
	}

	reason := payload.Reason
	if reason == "" {
		reason = "user_cancel"
	}
	interruptTimeout := runtimeInterruptTimeout(c.host.InterruptDrainTimeout)
	err := c.thread.Interrupt(c.ctx, threadpkg.TransportThreadInterruptRequest{
		Kind:             threadpkg.TransportThreadInterruptKindCancelInput,
		ControlMessageID: fmt.Sprint(message.MessageID),
		CutoffMessageID:  fmt.Sprint(payload.CutoffMessageID),
		Timeout:          &interruptTimeout,
	})
	if err != nil {
		if c.ctx.Err() != nil {
			return runResult{}
		}
		return runResult{reason: interruptFailedReason, err: fmt.Errorf("Thread.Interrupt cancel_input: %w", err)}
	}
	timedOut, err := c.waitForInterrupt(stop)
	if err != nil {
		if c.ctx.Err() != nil {
			return runResult{}
		}
		return runResult{reason: interruptFailedReason, err: err}
	}
	if timedOut {
		return runResult{reason: defaultInterruptTimeoutReason}
	}
	return c.ackMessage(message, "")
}

func (c *threadRun) handleClose(message *dalmodel.Message, pending *[]*dalmodel.Message, stop <-chan struct{}) (result runResult) {
	reason := defaultCloseThreadReason
	var payload CloseThreadControlPayload
	parseErr := json.Unmarshal(message.Payload, &payload)
	if parseErr == nil && payload.Reason != "" {
		reason = payload.Reason
	}
	*pending = nil

	if c.thread.ActiveRun() != nil {
		interruptTimeout := runtimeInterruptTimeout(c.host.InterruptDrainTimeout)
		_ = c.thread.Interrupt(c.ctx, threadpkg.TransportThreadInterruptRequest{
			Kind:             threadpkg.TransportThreadInterruptKindCloseThread,
			ControlMessageID: fmt.Sprint(message.MessageID),
			Timeout:          &interruptTimeout,
		})
		_, _ = c.waitForInterrupt(stop)
	}
	return runResult{reason: reason, closeMessageID: message.MessageID}
}

func (c *threadRun) waitForInterrupt(stop <-chan struct{}) (timedOut bool, err error) {
	timer := time.NewTimer(c.host.InterruptDrainTimeout)
	defer timer.Stop()
	poll := time.NewTicker(c.host.MessagePollInterval)
	defer poll.Stop()

	for {
		if c.thread.ActiveRun() == nil {
			return false, nil
		}
		select {
		case <-c.ctx.Done():
			return false, context.Cause(c.ctx)
		case <-stop:
			return false, nil
		case <-poll.C:
			continue
		case <-timer.C:
			return true, nil
		}
	}
}

// Run 先强制取消，给 Host 留出后半个等待窗口保存输出和清理。
func runtimeInterruptTimeout(drain time.Duration) time.Duration {
	return min(defaultRuntimeInterruptTimeout, drain/2)
}

func (c *threadRun) waitInput(stop <-chan struct{}, delay time.Duration) bool {
	if delay <= 0 {
		return false
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
		return true
	case <-stop:
		return true
	case <-c.acceptDone:
		return true
	case <-timer.C:
		return false
	}
}

func getPullErrorBackoff(base time.Duration, consecutiveErrors int) time.Duration {
	if consecutiveErrors <= 0 {
		return 0
	}
	if base <= 0 {
		base = defaultMessagePollInterval
	}
	backoff := base
	for i := 1; i < consecutiveErrors && backoff < maxPullErrorBackoff; i++ {
		backoff *= 2
	}
	if backoff > maxPullErrorBackoff {
		return maxPullErrorBackoff
	}
	return backoff
}

func dropCanceledMessages(messages []*dalmodel.Message, cutoff int64) (kept []*dalmodel.Message) {
	kept = messages[:0]
	for _, message := range messages {
		if message != nil && (message.IsControl() || message.MessageID > cutoff) {
			kept = append(kept, message)
		}
	}
	return kept
}
