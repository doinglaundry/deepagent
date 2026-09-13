// Package managed schedules leased Threads and owns the execution boundary.
package managed

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"eino-cli/manager/api"
	"eino-cli/protocol"
)

var ErrBusy = errors.New("runtime is draining; retry input")

type Runtime interface {
	PostMessage(context.Context, protocol.Input) (string, error)
	Cancel()
	Events() <-chan protocol.Event
	Close() error
}
type Factory func(context.Context, api.Claim) (Runtime, error)
type Config struct {
	ID              string        `yaml:"id"`
	Concurrency     int           `yaml:"concurrency"`
	PollInterval    time.Duration `yaml:"poll_interval"`
	PermitTTL       time.Duration `yaml:"permit_ttl"`
	ShutdownGrace   time.Duration `yaml:"shutdown_grace"`
	PublishAttempts int           `yaml:"publish_attempts"`
	Logger          *slog.Logger  `yaml:"-"`
}
type Worker struct {
	manager api.Manager
	factory Factory
	config  Config
}

func New(m api.Manager, f Factory, c Config) (*Worker, error) {
	if m == nil || f == nil {
		return nil, fmt.Errorf("manager and runtime factory required")
	}
	if c.Concurrency < 0 || c.PollInterval < 0 || c.PermitTTL < 0 || c.ShutdownGrace < 0 || c.PublishAttempts < 0 {
		return nil, fmt.Errorf("worker limits must not be negative")
	}
	if c.ID == "" {
		c.ID = protocol.NewID("worker")
	}
	if c.Concurrency == 0 {
		c.Concurrency = 4
	}
	if c.PollInterval == 0 {
		c.PollInterval = 200 * time.Millisecond
	}
	if c.PermitTTL == 0 {
		c.PermitTTL = 30 * time.Second
	}
	if c.PermitTTL < 3*time.Millisecond {
		return nil, fmt.Errorf("permit TTL must be at least 3ms")
	}
	if c.ShutdownGrace == 0 {
		c.ShutdownGrace = 30 * time.Second
	}
	if c.PublishAttempts == 0 {
		c.PublishAttempts = 3
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	return &Worker{manager: m, factory: f, config: c}, nil
}

// Run stops claiming on cancellation, renews running work during its grace period,
// and then cancels and drains remaining engines before returning.
func (w *Worker) Run(ctx context.Context) error {
	execCtx, cancelExec := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelExec()
	var wg sync.WaitGroup
	slots := make(chan struct{}, w.config.Concurrency)
	var active sync.Map
	scan := func() {
		if ctx.Err() != nil {
			return
		}
		candidates, err := w.manager.ScanRunnableThreads(ctx, w.config.Concurrency)
		if err != nil {
			if ctx.Err() == nil {
				w.config.Logger.Error("scan runnable threads", "error", err)
			}
			return
		}
		for _, candidate := range candidates {
			if ctx.Err() != nil {
				return
			}
			if _, loaded := active.LoadOrStore(candidate.ID, true); loaded {
				continue
			}
			select {
			case slots <- struct{}{}:
			default:
				active.Delete(candidate.ID)
				return
			}
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				defer func() { <-slots; active.Delete(id) }()
				claim, err := w.manager.ClaimThread(ctx, id, w.config.ID, w.config.PermitTTL)
				if err != nil {
					if !errors.Is(err, api.ErrConflict) && !errors.Is(err, api.ErrPermitLost) && ctx.Err() == nil {
						w.config.Logger.Error("claim thread", "thread", id, "error", err)
					}
					return
				}
				if err = w.execute(execCtx, claim); err != nil && !errors.Is(err, context.Canceled) {
					w.config.Logger.Error("execute thread", "thread", id, "error", err)
				}
			}(candidate.ID)
		}
	}
	scan()
	ticker := time.NewTicker(w.config.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()
			timer := time.NewTimer(w.config.ShutdownGrace)
			defer timer.Stop()
			select {
			case <-done:
				return nil
			case <-timer.C:
				cancelExec()
				<-done
				return nil
			}
		case <-ticker.C:
			scan()
		}
	}
}

func (w *Worker) execute(parent context.Context, claim api.Claim) (result error) {
	execCtx, cancelExec := context.WithCancel(parent)
	defer cancelExec()
	leaseCtx, cancelLease := context.WithCancel(context.WithoutCancel(parent))
	defer cancelLease()
	var lost atomic.Bool
	renewed := make(chan struct{})
	go func() {
		defer close(renewed)
		ticker := time.NewTicker(w.config.PermitTTL / 3)
		defer ticker.Stop()
		permit := claim.Permit
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				callCtx, cancel := context.WithTimeout(leaseCtx, w.config.PermitTTL/3)
				next, err := w.manager.RenewThreadPermit(callCtx, permit, w.config.PermitTTL)
				cancel()
				if err != nil {
					lost.Store(true)
					cancelExec()
					return
				}
				permit = next
			}
		}
	}()
	defer func() { cancelLease(); <-renewed }()
	runtime, err := w.factory(execCtx, claim)
	if err != nil {
		if lost.Load() {
			return errors.Join(err, api.ErrPermitLost)
		}
		return errors.Join(fmt.Errorf("build runtime: %w", err), w.failConstruction(leaseCtx, claim, err))
	}
	events := runtime.Events()
	active := map[string]bool{}
	closing := claim.Thread.State == api.Closing
	var block *protocol.Block
	publish := func(e protocol.Event) error {
		if lost.Load() {
			return api.ErrPermitLost
		}
		e.Namespace = claim.Thread.Namespace
		e.SessionID = claim.Thread.SessionID
		e.ThreadID = claim.Thread.ID
		if e.ID == "" {
			e.ID = protocol.NewID("event")
		}
		if e.CreatedAt.IsZero() {
			e.CreatedAt = time.Now().UTC()
		}
		var err error
		for attempt := 0; attempt < w.config.PublishAttempts; attempt++ {
			callCtx, cancel := context.WithTimeout(leaseCtx, w.config.PermitTTL/3)
			_, err = w.manager.PublishEvent(callCtx, claim.Permit, e)
			cancel()
			if err == nil {
				return nil
			}
			if errors.Is(err, api.ErrPermitLost) {
				lost.Store(true)
				cancelExec()
				return err
			}
			if attempt+1 < w.config.PublishAttempts {
				timer := time.NewTimer(time.Duration(attempt+1) * 25 * time.Millisecond)
				select {
				case <-leaseCtx.Done():
					timer.Stop()
					return leaseCtx.Err()
				case <-timer.C:
				}
			}
		}
		return fmt.Errorf("publish event %s: %w", e.ID, err)
	}
	consume := func(e protocol.Event) error {
		if e.Terminal() {
			delete(active, e.RunID)
			if e.Kind == protocol.EventBlocked {
				block = e.Block
			}
		}
		return publish(e)
	}
	// Close can wait for producers. Drain concurrently so a full event buffer cannot
	// deadlock shutdown, and retain ownership until all final events are published.
	defer func() {
		if errors.Is(result, api.ErrPermitLost) {
			lost.Store(true)
		}
		if len(active) > 0 || closing || execCtx.Err() != nil {
			runtime.Cancel()
		}
		closeDone := make(chan error, 1)
		go func() { closeDone <- runtime.Close() }()
		closeFinished := false
		for events != nil || !closeFinished {
			select {
			case event, ok := <-events:
				if !ok {
					events = nil
					continue
				}
				if !lost.Load() {
					if err := consume(event); err != nil {
						result = errors.Join(result, err)
					}
				}
			case err := <-closeDone:
				closeFinished = true
				closeDone = nil
				result = errors.Join(result, err)
			}
		}
		if lost.Load() {
			result = errors.Join(result, api.ErrPermitLost)
			return
		}
		callCtx, cancel := context.WithTimeout(leaseCtx, w.config.PermitTTL/3)
		defer cancel()
		if closing {
			_, err := w.manager.PublishEvent(callCtx, claim.Permit, protocol.Event{ID: protocol.NewID("event"), Namespace: claim.Thread.Namespace, SessionID: claim.Thread.SessionID, ThreadID: claim.Thread.ID, Kind: protocol.EventThreadClosed, CreatedAt: time.Now().UTC()})
			if err != nil {
				result = errors.Join(result, err)
				return
			}
			result = errors.Join(result, w.manager.ConfirmThreadClosed(callCtx, claim.Permit))
			return
		}
		result = errors.Join(result, w.manager.ReleaseThread(callCtx, claim.Permit, api.Release{Block: block}))
	}()
	deliver := func(inputs []protocol.Input) error {
		for _, input := range inputs {
			if execCtx.Err() != nil {
				return execCtx.Err()
			}
			switch input.Kind {
			case protocol.InputClose:
				closing = true
				return nil
			case protocol.InputCancel:
				runtime.Cancel()
			default:
				runID, err := runtime.PostMessage(execCtx, input)
				if errors.Is(err, ErrBusy) {
					return nil
				}
				if err != nil {
					if execCtx.Err() != nil {
						return execCtx.Err()
					}
					event := protocol.Event{Kind: protocol.EventRunFailed, RunID: protocol.NewID("run"), MessageIDs: []string{input.ID}, Error: "Worker rejected input: " + err.Error()}
					if input.Kind == protocol.InputCompact {
						event.Kind = protocol.EventCompacted
						event.RunID = ""
					}
					if input.Kind == protocol.InputResume && input.Resume != nil {
						event.RunID = input.Resume.RunID
						event.Block = &protocol.Block{RunID: input.Resume.RunID, CheckpointID: input.Resume.CheckpointID, InterruptID: input.Resume.InterruptID, Kind: "worker_error", Question: "Worker could not restore this execution: " + err.Error()}
					}
					// The failure is the durable disposition. Acknowledge only after it commits;
					// this avoids replaying a rejected input on every lease acquisition.
					if err = publish(event); err != nil {
						return err
					}
					if event.Block != nil {
						block = event.Block
					}
				} else if runID != "" {
					active[runID] = true
				}
			}
			if err := w.manager.ConfirmInputDelivery(execCtx, claim.Permit, input.ID); err != nil {
				return err
			}
			if block != nil {
				return nil
			}
		}
		return nil
	}
	if err := deliver(claim.Inputs); err != nil {
		return err
	}
	ticker := time.NewTicker(w.config.PollInterval)
	defer ticker.Stop()
	for {
		// Prioritize buffered output over release. Close drains any last producer output.
		for events != nil {
			select {
			case e, ok := <-events:
				if !ok {
					events = nil
				} else if err := consume(e); err != nil {
					return err
				}
			default:
				goto drained
			}
		}
	drained:
		if closing || block != nil || len(active) == 0 {
			return nil
		}
		select {
		case <-execCtx.Done():
			return execCtx.Err()
		case e, ok := <-events:
			if !ok {
				return fmt.Errorf("runtime events closed while run active")
			}
			if err := consume(e); err != nil {
				return err
			}
		case <-ticker.C:
			inputs, err := w.manager.ReadPendingInputs(execCtx, claim.Permit)
			if err != nil {
				return err
			}
			if err = deliver(inputs); err != nil {
				return err
			}
		}
	}
}

// failConstruction records a request-correlated failure before returning the
// thread. Resume failures retain their checkpoint correlation for a later retry.
func (w *Worker) failConstruction(ctx context.Context, claim api.Claim, cause error) error {
	runID := protocol.NewID("run")
	var ids []string
	var block *protocol.Block
	closing := claim.Thread.State == api.Closing
	for _, in := range claim.Inputs {
		switch in.Kind {
		case protocol.InputUser, protocol.InputCompact:
			ids = append(ids, in.ID)
		case protocol.InputResume:
			ids = append(ids, in.ID)
			if in.Resume != nil {
				runID = in.Resume.RunID
				block = &protocol.Block{RunID: in.Resume.RunID, CheckpointID: in.Resume.CheckpointID, InterruptID: in.Resume.InterruptID, Kind: "worker_error", Question: "Worker could not restore this execution: " + cause.Error()}
			}
		case protocol.InputClose:
			closing = true
		case protocol.InputCancel:
			callCtx, cancel := context.WithTimeout(ctx, w.config.PermitTTL/3)
			err := w.manager.ConfirmInputDelivery(callCtx, claim.Permit, in.ID)
			cancel()
			if err != nil {
				return err
			}
		}
	}
	publish := func(event protocol.Event) error {
		event.ID = protocol.NewID("event")
		event.Namespace = claim.Thread.Namespace
		event.SessionID = claim.Thread.SessionID
		event.ThreadID = claim.Thread.ID
		event.CreatedAt = time.Now().UTC()
		var err error
		for attempt := 0; attempt < w.config.PublishAttempts; attempt++ {
			callCtx, cancel := context.WithTimeout(ctx, w.config.PermitTTL/3)
			_, err = w.manager.PublishEvent(callCtx, claim.Permit, event)
			cancel()
			if err == nil || errors.Is(err, api.ErrPermitLost) {
				return err
			}
		}
		return err
	}
	if closing {
		if err := publish(protocol.Event{Kind: protocol.EventThreadClosed}); err != nil {
			return err
		}
		callCtx, cancel := context.WithTimeout(ctx, w.config.PermitTTL/3)
		defer cancel()
		return w.manager.ConfirmThreadClosed(callCtx, claim.Permit)
	}
	if len(ids) > 0 {
		if err := publish(protocol.Event{Kind: protocol.EventRunFailed, RunID: runID, MessageIDs: ids, Error: "Worker initialization failed: " + cause.Error(), Block: block}); err != nil {
			return err
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, w.config.PermitTTL/3)
	defer cancel()
	return w.manager.ReleaseThread(callCtx, claim.Permit, api.Release{Block: block})
}
