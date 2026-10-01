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

// Config controls scheduling, leases, polling and shutdown.
type Config struct {
	Concurrency         int           `yaml:"concurrency"`
	LeaseMS             int64         `yaml:"lease_ms"`
	ScanInterval        time.Duration `yaml:"scan_interval"`
	MessagePollInterval time.Duration `yaml:"message_poll_interval"`
	IdleTimeout         time.Duration `yaml:"idle_timeout"`

	// Worker 退出时，等待当前 Run 自然完成。
	ShutdownDrainTimeout time.Duration `yaml:"shutdown_drain_timeout"`
	// 退出时中断后的等待窗口，也用于等待 Thread.Close 完成。
	ShutdownInterruptDrainTimeout time.Duration `yaml:"shutdown_interrupt_drain_timeout"`
	// 用户取消或关闭 Thread 后，等待 Run 停止。
	InterruptDrainTimeout time.Duration `yaml:"interrupt_drain_timeout"`
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
	if w.LeaseMS <= 0 {
		w.LeaseMS = defaultLeaseMS
	}
	if w.ScanInterval <= 0 {
		w.ScanInterval = defaultScanInterval
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
		claim, acquireErr := w.Client.Acquire(ctx, manager.AcquireRequest{LeaseMS: w.LeaseMS})
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
	deadline := lease.LeaseUntil
	if deadline.IsZero() {
		deadline = time.Now().Add(time.Duration(defaultLeaseMS) * time.Millisecond)
	}
	// 使用实际剩余租期，避免续租间隔超过 Manager 授予的租约。
	renewTimer := time.NewTimer(max(time.Until(deadline)/3, time.Nanosecond))
	defer renewTimer.Stop()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
			return fmt.Errorf("lease expired thread_id=%d: %w", lease.ThreadID, context.DeadlineExceeded)
		case <-renewTimer.C:
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
			renewTimer.Reset(max(time.Until(deadline)/3, time.Nanosecond))
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
