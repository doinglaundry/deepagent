//go:build !windows

package threadhost

import (
	"context"
	"errors"
	"fmt"
	"time"

	deepagents "eino-cli/deepagent/core"
	"eino-cli/deepagent/helper/serialiser"
	"eino-cli/deepagent/manager"
)

type runResult struct {
	reason         string
	err            error
	closeMessageID int64
	outputFailed   bool
}

func (r runResult) empty() bool {
	return r.reason == "" && r.err == nil && r.closeMessageID == 0
}

// threadRun owns only state that spans one claimed runtime execution.
type threadRun struct {
	host       *ThreadHost
	ctx        context.Context
	acceptDone <-chan struct{}
	claim      *manager.AcquireResult
	thread     *deepagents.Thread
	idleSince  time.Time
	wasActive  bool
}

func (c *threadRun) run(items <-chan deepagents.TransportThreadOutputItem) (result runResult, closeErr error) {
	stop := make(chan struct{})
	stopOutput := make(chan struct{})
	activity := make(chan time.Time, 1)
	inputResults := make(chan runResult, 1)
	inputDone := make(chan struct{})
	outputSignal := make(chan struct{}, 1)
	outputDone := make(chan runResult, 1)

	go c.runInput(stop, activity, inputResults, inputDone)
	go c.runOutput(stopOutput, items, outputSignal, outputDone)
	requested, input := c.wait(activity, inputResults, outputSignal, stop)
	close(stop)
	<-inputDone
	// Close owns cleanup even if the caller's grace period expires. The output
	// consumer stays alive until all producers stop, so terminal sends can finish.
	closed := make(chan error, 1)
	go func() { closed <- c.thread.Close(context.WithoutCancel(c.ctx)) }()
	timer := time.NewTimer(c.host.ShutdownInterruptDrainTimeout)
	defer timer.Stop()
	select {
	case closeErr = <-closed:
		close(stopOutput)
	case <-timer.C:
		go func() {
			<-closed
			close(stopOutput)
		}()
		return requested, context.DeadlineExceeded
	}
	output := <-outputDone
	if input.empty() {
		select {
		case input = <-inputResults:
		default:
		}
	}

	if output.outputFailed {
		return output, closeErr
	}
	if input.closeMessageID != 0 || input.err != nil {
		return input, closeErr
	}
	if !output.empty() {
		return output, closeErr
	}
	if !input.empty() {
		return input, closeErr
	}
	if !requested.empty() {
		return requested, closeErr
	}
	return runResult{reason: defaultReleaseReason}, closeErr
}

func (c *threadRun) wait(activity <-chan time.Time, inputResults <-chan runResult, outputSignal <-chan struct{}, stop <-chan struct{}) (requested runResult, input runResult) {
	idleTicker := time.NewTicker(c.host.MessagePollInterval)
	defer idleTicker.Stop()

	for {
		select {
		case <-c.acceptDone:
			return c.drainShutdown(inputResults, outputSignal, stop), runResult{}
		default:
		}
		{
			result := c.checkIdleRelease(activity)
			if !result.empty() {
				return result, runResult{}
			}
		}

		select {
		case input = <-inputResults:
			return runResult{}, input
		case <-outputSignal:
			return runResult{}, runResult{}
		case <-c.ctx.Done():
			return runResult{reason: defaultErrorReleaseReason, err: context.Cause(c.ctx)}, runResult{}
		case <-c.acceptDone:
			return c.drainShutdown(inputResults, outputSignal, stop), runResult{}
		case <-idleTicker.C:
			continue
		}
	}
}

func (c *threadRun) checkIdleRelease(activity <-chan time.Time) (result runResult) {
	select {
	case c.idleSince = <-activity:
		c.wasActive = c.thread.ActiveRun() != nil
	default:
	}
	active := c.thread.ActiveRun() != nil
	if active {
		c.wasActive = true
		return runResult{}
	}
	if c.wasActive {
		c.wasActive = false
		c.idleSince = time.Now()
		return runResult{}
	}
	if time.Since(c.idleSince) < c.host.IdleTimeout {
		return runResult{}
	}
	return runResult{reason: defaultReleaseReason}
}

func (c *threadRun) drainShutdown(inputResults <-chan runResult, outputSignal <-chan struct{}, stop <-chan struct{}) (result runResult) {
	timer := time.NewTimer(c.host.ShutdownDrainTimeout)
	defer timer.Stop()
	poll := time.NewTicker(c.host.MessagePollInterval)
	defer poll.Stop()

	for {
		if c.thread.ActiveRun() == nil {
			return runResult{reason: defaultGracefulReleaseReason}
		}

		select {
		case result = <-inputResults:
			return result
		case <-outputSignal:
			return runResult{}
		case <-c.ctx.Done():
			return runResult{reason: defaultErrorReleaseReason, err: context.Cause(c.ctx)}
		case <-stop:
			return runResult{}
		case <-poll.C:
			continue
		case <-timer.C:
			if c.thread.ActiveRun() != nil {
				c.interruptShutdownTimeout()
			}
			return c.waitForShutdownOutput(inputResults, outputSignal, stop)
		}
	}
}

func (c *threadRun) interruptShutdownTimeout() {
	interruptTimeout := runtimeInterruptTimeout(c.host.ShutdownInterruptDrainTimeout)
	_ = c.thread.Interrupt(c.ctx, deepagents.TransportThreadInterruptRequest{
		Kind:    deepagents.TransportThreadInterruptKindWorkerShutdownTimeout,
		Reason:  defaultShutdownTimeoutReason,
		Timeout: &interruptTimeout,
	})
}

func (c *threadRun) waitForShutdownOutput(inputResults <-chan runResult, outputSignal <-chan struct{}, stop <-chan struct{}) (result runResult) {
	timer := time.NewTimer(c.host.ShutdownInterruptDrainTimeout)
	defer timer.Stop()

	select {
	case result = <-inputResults:
		return result
	case <-outputSignal:
		return runResult{}
	case <-c.ctx.Done():
		return runResult{reason: defaultErrorReleaseReason, err: context.Cause(c.ctx)}
	case <-stop:
		return runResult{}
	case <-timer.C:
		return runResult{reason: defaultShutdownTimeoutReason}
	}
}

func (w *ThreadHost) closeThread(ctx context.Context, thread *deepagents.Thread) error {
	timeout := w.ShutdownInterruptDrainTimeout
	if timeout <= 0 {
		timeout = defaultShutdownInterruptDrain
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	return thread.Close(cleanupCtx)
}

// finish commits ownership changes only after output persistence and cleanup.
func (c *threadRun) finish(result runResult, closeErr error, waitLease func() error) error {
	if closeErr != nil {
		var leaseErr error
		if c.ctx.Err() != nil {
			leaseErr = waitLease()
			if leaseErr == nil {
				leaseErr = context.Cause(c.ctx)
			}
		}
		return errors.Join(result.err, closeErr, leaseErr)
	}
	if c.ctx.Err() != nil {
		leaseErr := waitLease()
		if leaseErr == nil {
			leaseErr = context.Cause(c.ctx)
		}
		return errors.Join(result.err, leaseErr)
	}
	if result.outputFailed {
		return result.err
	}
	lease := c.claim.Lease
	if result.closeMessageID != 0 {
		_, err := c.host.Client.ConfirmThreadClosed(c.ctx, lease.ThreadID, lease.LeaseToken, result.closeMessageID)
		return serialiser.WrapError(fmt.Sprintf("CompleteCloseThread thread_id=%d control_message_id=%d", lease.ThreadID, result.closeMessageID), err)
	}
	_, releaseErr := c.host.Client.ReleaseThread(c.ctx, lease.ThreadID, lease.LeaseToken)
	releaseErr = serialiser.WrapError(fmt.Sprintf("ReleaseThread thread_id=%d", lease.ThreadID), releaseErr)
	return errors.Join(result.err, closeErr, releaseErr)
}
