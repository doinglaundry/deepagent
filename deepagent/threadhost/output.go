//go:build !windows

package threadhost

import (
	"context"
	"fmt"
	"maps"
	"strconv"
	"strings"

	deepagents "eino-cli/deepagent/core"
	dalmodel "eino-cli/deepagent/dal/model"
	"eino-cli/deepagent/helper/serialiser"
	"eino-cli/deepagent/manager"
)

func (c *threadRun) runOutput(stop <-chan struct{}, items <-chan deepagents.TransportThreadOutputItem, signal chan<- struct{}, done chan<- runResult) {
	result := runResult{}
	defer func() { done <- result }()
	for {
		select {
		case <-stop:
			c.drainOutput(items, signal, &result)
			return
		case item, ok := <-items:
			if !ok {
				select {
				case signal <- struct{}{}:
				default:
				}
				return
			}
			c.handleOutput(item, signal, &result)
		}
	}
}

// handleOutput persists an event before recording the first runtime yield.
func (c *threadRun) handleOutput(item deepagents.TransportThreadOutputItem, signal chan<- struct{}, result *runResult) {
	if result.outputFailed {
		return
	}
	if item.Err != nil {
		*result = runResult{reason: defaultErrorReleaseReason, err: item.Err, outputFailed: true}
		select {
		case signal <- struct{}{}:
		default:
		}
		return
	}
	if item.Event != nil {
		err := c.host.saveThreadOutput(c.ctx, c.claim.Thread.ThreadID, item.Event, c.claim.Lease.LeaseToken)
		if err != nil {
			*result = runResult{reason: defaultErrorReleaseReason, err: err, outputFailed: true}
			select {
			case signal <- struct{}{}:
			default:
			}
			return
		}
	}

	yield := item.Yield
	if yield == nil {
		return
	}

	if result.empty() {
		*result = runResult{reason: yield.Reason, err: yield.Err}
	}
	select {
	case signal <- struct{}{}:
	default:
	}
}

func (c *threadRun) drainOutput(items <-chan deepagents.TransportThreadOutputItem, signal chan<- struct{}, result *runResult) {
	for {
		select {
		case item, ok := <-items:
			if !ok {
				return
			}
			c.handleOutput(item, signal, result)
		default:
			return
		}
	}
}

func (w *ThreadHost) saveThreadOutput(ctx context.Context, threadID int64, event *deepagents.TransportEvent, leaseToken string) (resultErr error) {
	if event.ThreadID == "" {
		event.ThreadID = fmt.Sprint(threadID)
	}
	eventThreadID, err := serialiser.ParseNonzeroID(event.ThreadID, "event_thread_id")
	if err == nil {
		switch {
		case eventThreadID != threadID:
			err = fmt.Errorf("append event thread mismatch: event_thread_id=%d run_thread_id=%d", eventThreadID, threadID)
		case event.RunID == "":
			err = fmt.Errorf("append event turn_id is required: thread_id=%d", threadID)
		case event.Type == "":
			err = fmt.Errorf("append event event_type is required: thread_id=%d turn_id=%s", threadID, event.RunID)
		}
	}
	if err != nil {

		return err
	}
	outputs := []manager.OutputFrame{*ProtocolToOutputFrame(threadID, event)}
	for attempt := 1; attempt <= defaultAppendEventAttempts; attempt++ {
		err = ctx.Err()
		if err != nil {

			return err
		}
		publishErr := w.Client.SaveOutput(ctx, threadID, leaseToken, event.RunID, outputs)
		err = serialiser.WrapError(fmt.Sprintf("AppendEvents thread_id=%d turn_id=%s event_type=%s", threadID, event.RunID, event.Type), publishErr)
		if err == nil {

			return err
		}

		if attempt < defaultAppendEventAttempts {
			sleepErr := sleepContext(ctx, defaultAppendEventRetryDelay)
			if sleepErr != nil {

				return sleepErr
			}
		}
	}

	return err
}

func ProtocolToWorkerMessage(message *dalmodel.Message) (result *deepagents.TransportMessage) {
	if message == nil {
		return nil
	}
	return &deepagents.TransportMessage{
		ID:       fmt.Sprint(message.MessageID),
		Sender:   protocolSenderFromManager(message.Sender),
		Type:     deepagents.TransportMessageType(message.MessageType),
		Payload:  append([]byte(nil), message.Payload...),
		Metadata: maps.Clone(message.Metadata),
	}
}

func protocolSenderFromManager(sender *dalmodel.Sender) (result *deepagents.TransportSender) {
	if sender == nil {
		return nil
	}
	return &deepagents.TransportSender{
		Type: deepagents.TransportSenderType(strings.ToUpper(string(sender.Type))),
		ID:   sender.ID,
	}
}

func ProtocolToOutputFrame(threadID int64, event *deepagents.TransportEvent) (result *manager.OutputFrame) {
	if event == nil {
		return nil
	}
	managerEvent := &manager.OutputFrame{
		ThreadID:  threadID,
		RunID:     event.RunID,
		EventType: string(event.Type),
		Payload:   append([]byte(nil), event.Payload...),
		Metadata:  maps.Clone(event.Metadata),
	}
	if event.ID != "" {
		{
			id, err := strconv.ParseInt(strings.TrimSpace(event.ID), 10, 64)
			if err == nil {
				managerEvent.EventID = id
			}
		}
	}
	if !event.TS.IsZero() {
		managerEvent.CreatedAt = event.TS
	}
	return managerEvent
}
