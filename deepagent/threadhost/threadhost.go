//go:build !windows

package threadhost

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"eino-cli/deepagent/helper/serialiser"
	"eino-cli/deepagent/manager"
)

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

	thread, output, err := w.createThread(runCtx, claim.Thread)
	if err != nil {
		err = fmt.Errorf("create Thread thread_id=%d: %w", claim.Thread.ThreadID, err)
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
		_, releaseErr := w.Client.ReleaseThread(runCtx, claim.Lease.ThreadID, claim.Lease.LeaseToken)
		releaseErr = serialiser.WrapError(fmt.Sprintf("ReleaseThread thread_id=%d", claim.Lease.ThreadID), releaseErr)
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
