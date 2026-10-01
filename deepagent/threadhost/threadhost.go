//go:build !windows

package threadhost

import (
	"context"
	deepagents "eino-cli/deepagent/core"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"

	dalmodel "eino-cli/deepagent/dal/model"
	"eino-cli/deepagent/helper/serialiser"
	"eino-cli/deepagent/manager"
)

// 01 Core Types

const (
	defaultConcurrency             = 1
	defaultScanLimit               = int32(20)
	defaultLeaseMS                 = int64(60_000)
	defaultScanInterval            = time.Second
	defaultMessagePollInterval     = 500 * time.Millisecond
	defaultIdleTimeout             = 10 * time.Second
	defaultShutdownDrainTimeout    = 120 * time.Second
	defaultShutdownInterruptDrain  = 120 * time.Second
	defaultInterruptDrainTimeout   = 30 * time.Second
	defaultRuntimeInterruptTimeout = 15 * time.Second
	defaultAppendEventAttempts     = 3
	defaultAppendEventRetryDelay   = 100 * time.Millisecond
	defaultReleaseReason           = "agent thread completed"
	defaultErrorReleaseReason      = "agent thread failed"
	postMessageFailedReason        = "agent thread post message failed"
	ackMessageFailedReason         = "agent thread ack failed"
	controlInputFailedReason       = "agent thread control input failed"
	interruptFailedReason          = "agent thread interrupt failed"
	defaultGracefulReleaseReason   = "worker graceful exit"
	defaultShutdownTimeoutReason   = "worker graceful exit timeout"
	defaultInterruptTimeoutReason  = "agent thread interrupt timeout"
	defaultThreadClosedReason      = "agent thread closed"
	defaultCloseThreadReason       = "user_close"
	maxPullErrorBackoff            = 5 * time.Second
)

// Config controls scheduling, leases, polling and shutdown.
type Config struct {
	Concurrency int    `yaml:"concurrency"`
	ScanLimit   int32  `yaml:"scan_limit"`
	LeaseMS     int64  `yaml:"lease_ms"`
	LeaseOwner  string `yaml:"lease_owner"`

	ScanInterval        time.Duration `yaml:"scan_interval"`
	RenewInterval       time.Duration `yaml:"renew_interval"`
	MessagePollInterval time.Duration `yaml:"message_poll_interval"`
	// IdleTimeout is the normal-operation quiet period before a claimed thread
	// is released after it becomes inactive.
	IdleTimeout time.Duration `yaml:"idle_timeout"`
	// ShutdownDrainTimeout is the maximum time a shutting-down worker waits for
	// an already active run to finish naturally. During this window the worker
	// keeps renewing the lease and appending runtime output, but it does not pull
	// or deliver new pending messages.
	ShutdownDrainTimeout time.Duration `yaml:"shutdown_drain_timeout"`
	// ShutdownInterruptDrainTimeout is the final grace period after shutdown
	// drain timed out and the worker asked the runtime to interrupt the active
	// run. Runtime output is still consumed during this window so business code
	// can emit its own terminal event.
	ShutdownInterruptDrainTimeout time.Duration `yaml:"shutdown_interrupt_drain_timeout"`
	// InterruptDrainTimeout is the maximum time the worker waits for a runtime
	// to become inactive after a user/control-plane interrupt request. During
	// this window the worker keeps appending runtime output but does not deliver
	// new ordinary input.
	InterruptDrainTimeout time.Duration `yaml:"interrupt_drain_timeout"`
	// RuntimeInterruptTimeout is passed to the runtime when interrupting a
	// running run. If the run is still blocked after this duration, the
	// runtime may return an interrupted result before the blocked call exits.
	// If unset, the worker uses 15s capped below the active drain window.
	RuntimeInterruptTimeout time.Duration `yaml:"runtime_interrupt_timeout"`
}

// ThreadHost owns scanning, claims, input delivery and lease release.
type ThreadHost struct {
	Config
	Client  manager.Client
	Runtime RuntimeConfig
	Deps    RuntimeDeps
}

var _ manager.Client = (*manager.Manager)(nil)

var (
	ErrMissingClient  = errors.New("agentworker/cloud: client is required")
	ErrMissingRuntime = errors.New("agentworker/cloud: runtime models are required")
	ErrMissingThread  = errors.New("agentworker/cloud: thread is required")
	ErrMissingLease   = errors.New("agentworker/cloud: lease is required")
)

func (w *ThreadHost) normalize() {
	if w.Concurrency <= 0 {
		w.Concurrency = defaultConcurrency
	}
	if w.ScanLimit <= 0 {
		w.ScanLimit = defaultScanLimit
	}
	if w.LeaseMS <= 0 {
		w.LeaseMS = defaultLeaseMS
	}
	if w.ScanInterval <= 0 {
		w.ScanInterval = defaultScanInterval
	}
	if w.RenewInterval <= 0 {
		w.RenewInterval = time.Duration(w.LeaseMS) * time.Millisecond / 3
		if w.RenewInterval <= 0 {
			w.RenewInterval = defaultScanInterval
		}
	}
	if w.MessagePollInterval <= 0 {
		w.MessagePollInterval = defaultMessagePollInterval
	}
	if w.IdleTimeout <= 0 {
		w.IdleTimeout = defaultIdleTimeout
	}
	if w.ShutdownDrainTimeout <= 0 {
		w.ShutdownDrainTimeout = defaultShutdownDrainTimeout
	}
	if w.ShutdownInterruptDrainTimeout <= 0 {
		w.ShutdownInterruptDrainTimeout = defaultShutdownInterruptDrain
	}
	if w.InterruptDrainTimeout <= 0 {
		w.InterruptDrainTimeout = defaultInterruptDrainTimeout
	}
}

func (w *ThreadHost) Validate() error {
	if w == nil {
		return errors.New("agentworker: worker is nil")
	}
	if w.Client == nil {
		return ErrMissingClient
	}
	if len(w.Runtime.Models) == 0 {
		return ErrMissingRuntime
	}
	return nil
}

// 02 Worker Main Loop

// Run 在有空闲执行槽位时领取任务，再启动 Thread。
func (w *ThreadHost) Run(ctx context.Context) (err error) {
	if w != nil {
		w.normalize()
	}
	err = w.Validate()
	if err != nil {
		return err
	}
	sem := make(chan struct{}, w.Concurrency)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case sem <- struct{}{}:
		}
		claim, acquireErr := w.Client.Acquire(ctx, manager.AcquireRequest{LeaseMS: w.LeaseMS, ScanLimit: w.ScanLimit})
		if acquireErr != nil || claim.Thread == nil {
			<-sem
			err = sleepContext(ctx, w.ScanInterval)
			if err != nil {
				return err
			}
			continue
		}
		wg.Add(1)
		go func(claim manager.AcquireResult) {
			defer wg.Done()
			defer func() { <-sem }()
			_ = w.RunThread(context.WithoutCancel(ctx), ctx, &claim)
		}(claim)
	}
}

// RunThread creates the Thread, renews ownership, drives input/output, then closes and releases it.
func (w *ThreadHost) RunThread(ctx context.Context, acceptCtx context.Context, claim *manager.AcquireResult) (err error) {
	if w != nil {
		w.normalize()
	}
	err = w.Validate()
	if err != nil {
		return err
	}
	if claim == nil || claim.Thread == nil {
		return ErrMissingThread
	}
	if claim.Lease == nil {
		return ErrMissingLease
	}
	if !claim.Lease.LeaseUntil.IsZero() && !claim.Lease.LeaseUntil.After(time.Now()) {
		return fmt.Errorf("lease expired thread_id=%d: %w", claim.Lease.ThreadID, context.DeadlineExceeded)
	}

	runCtx, stopLease, waitLease := w.startLease(ctx, claim.Lease)
	defer stopLease()

	thread, output, err := w.createRuntime(runCtx, claim.Thread)
	if err != nil {
		var closeErr error
		if thread != nil {
			closeErr = w.closeThread(runCtx, thread)
		}
		if closeErr != nil {
			return errors.Join(err, closeErr)
		}
		if runCtx.Err() != nil {
			leaseErr := waitLease()
			if leaseErr == nil {
				leaseErr = context.Cause(runCtx)
			}
			return errors.Join(err, leaseErr)
		}
		releaseErr := w.releaseThread(runCtx, claim.Lease)
		return errors.Join(err, closeErr, releaseErr)
	}

	active := thread.ActiveRun()
	run := &threadRun{
		host: w, ctx: runCtx, acceptDone: acceptCtx.Done(), claim: claim, thread: thread,
		idleSince: time.Now(), wasActive: active != nil,
	}
	result, closeErr := run.run(output.Items)
	return run.finish(result, closeErr, waitLease)
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

func (w *ThreadHost) createRuntime(ctx context.Context, threadInfo *dalmodel.Thread) (thread *deepagents.Thread, output *deepagents.TransportThreadOutput, err error) {
	thread, err = w.createThread(ctx, threadInfo)
	if err != nil {
		return nil, nil, fmt.Errorf("create Thread thread_id=%d: %w", threadInfo.ThreadID, err)
	}

	output, err = thread.Init(ctx)
	if err != nil {
		return thread, nil, fmt.Errorf("Thread.Init thread_id=%d: %w", threadInfo.ThreadID, err)
	}
	if output == nil {
		output = &deepagents.TransportThreadOutput{}
	}
	return thread, output, nil
}

// 03 Lease / Ownership

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

func (w *ThreadHost) releaseThread(ctx context.Context, lease *manager.Lease) error {
	_, err := w.Client.ReleaseThread(ctx, lease.ThreadID, lease.LeaseToken)
	return serialiser.WrapError(fmt.Sprintf("ReleaseThread thread_id=%d", lease.ThreadID), err)
}

func (w *ThreadHost) getRuntimeInterruptTimeout() time.Duration {
	return w.getRuntimeInterruptTimeoutForDrain(w.InterruptDrainTimeout)
}

func (w *ThreadHost) getRuntimeInterruptTimeoutForDrain(drain time.Duration) time.Duration {
	timeout := w.RuntimeInterruptTimeout
	if timeout <= 0 {
		timeout = defaultRuntimeInterruptTimeout
	}
	if drain > 0 && timeout >= drain {
		adjusted := drain / 2
		if adjusted > 0 {
			return adjusted
		}
		return drain
	}
	return timeout
}

func (w *ThreadHost) confirmThreadClose(ctx context.Context, lease *manager.Lease, controlMessageID int64) (err error) {
	_, err = w.Client.ConfirmThreadClosed(ctx, lease.ThreadID, lease.LeaseToken, controlMessageID)
	err = serialiser.WrapError(fmt.Sprintf("CompleteCloseThread thread_id=%d control_message_id=%d", lease.ThreadID, controlMessageID), err)
	if err != nil {
		return err
	}

	return nil
}

func (w *ThreadHost) startLease(ctx context.Context, lease *manager.Lease) (runCtx context.Context, stop func(), wait func() error) {
	runCtx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	var leaseErr error
	go func() {
		defer close(done)
		leaseErr = w.renewLease(runCtx, lease)
		if leaseErr != nil {
			cancel(leaseErr)
		}
	}()
	stop = func() {
		cancel(nil)
		<-done
	}
	wait = func() (err error) {
		<-done
		return leaseErr
	}
	return runCtx, stop, wait
}

func (w *ThreadHost) renewLease(ctx context.Context, lease *manager.Lease) (err error) {
	ticker := time.NewTicker(w.RenewInterval)
	defer ticker.Stop()
	deadline := lease.LeaseUntil
	if deadline.IsZero() {
		deadline = time.Now().Add(time.Duration(defaultLeaseMS) * time.Millisecond)
	}
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			return fmt.Errorf("lease expired thread_id=%d: %w", lease.ThreadID, context.DeadlineExceeded)
		case <-ticker.C:
			renewCtx, cancel := context.WithDeadline(ctx, deadline)
			renewed, renewErr := w.Client.Renew(renewCtx, lease.ThreadID, lease.LeaseToken, w.LeaseMS)
			cancel()
			if !time.Now().Before(deadline) {
				return fmt.Errorf("lease expired thread_id=%d: %w", lease.ThreadID, context.DeadlineExceeded)
			}
			err = serialiser.WrapError(fmt.Sprintf("RenewThreadLease thread_id=%d", lease.ThreadID), renewErr)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			if renewed == nil || renewed.ThreadID != lease.ThreadID || renewed.LeaseToken != lease.LeaseToken || !renewed.LeaseUntil.After(time.Now()) {
				return ErrMissingLease
			}
			deadline = renewed.LeaseUntil
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(time.Until(deadline))
		}
	}
}

func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

const (
	// MessageTypeControlCancelInput is the Manager mailbox message type
	// used by control-plane callers to cancel input up to a cutoff message.
	MessageTypeControlCancelInput = dalmodel.ControlMessageTypeCancelInput
	// MessageTypeControlCloseThread is the Manager mailbox message type
	// used by control-plane callers to close a thread.
	MessageTypeControlCloseThread = dalmodel.ControlMessageTypeCloseThread
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

// 04 Claim Lifecycle

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
	if result.closeMessageID != 0 {
		return errors.Join(closeErr, c.host.confirmThreadClose(c.ctx, c.claim.Lease, result.closeMessageID))
	}
	releaseErr := c.host.releaseThread(c.ctx, c.claim.Lease)
	return errors.Join(result.err, closeErr, releaseErr)
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
	interruptTimeout := c.host.getRuntimeInterruptTimeoutForDrain(c.host.ShutdownInterruptDrainTimeout)
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
		if errors.Is(err, deepagents.TransportErrThreadClosed) {
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
	{
		err := json.Unmarshal(message.Payload, &payload)
		if err != nil || payload.CutoffMessageID <= 0 {
			if err == nil {
				err = fmt.Errorf("cancel input control missing cutoff_message_id")
			}
			*pending = nil
			result = c.ackMessage(message, "")
			if !result.empty() {
				return result
			}
			return runResult{reason: controlInputFailedReason, err: err}
		}
	}

	*pending = dropCanceledMessages(*pending, payload.CutoffMessageID)
	if c.thread.ActiveRun() == nil {
		return c.ackMessage(message, "")
	}

	reason := payload.Reason
	if reason == "" {
		reason = "user_cancel"
	}
	interruptTimeout := c.host.getRuntimeInterruptTimeout()
	err := c.thread.Interrupt(c.ctx, deepagents.TransportThreadInterruptRequest{
		Kind:             deepagents.TransportThreadInterruptKindCancelInput,
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
		interruptTimeout := c.host.getRuntimeInterruptTimeout()
		_ = c.thread.Interrupt(c.ctx, deepagents.TransportThreadInterruptRequest{
			Kind:             deepagents.TransportThreadInterruptKindCloseThread,
			ControlMessageID: fmt.Sprint(message.MessageID),
			Timeout:          &interruptTimeout,
		})
		_, _ = c.waitForInterrupt(stop)
	}
	return runResult{reason: reason, closeMessageID: message.MessageID}
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

// 07 Manager <-> Runtime Protocol

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
