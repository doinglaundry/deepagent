// Package runtime connects CLI operations to the shared Manager. It never builds a model.
package runtime

import (
	"context"
	"eino-cli/manager/api"
	"eino-cli/protocol"
	"fmt"
	"sync"
	"time"
)

type Config struct {
	SessionID, ThreadID, WorkDir, Name string
	PlanMode                           bool
	PollInterval                       time.Duration
}
type Runtime struct {
	lifetime    context.Context
	detach      context.CancelFunc
	manager     api.Manager
	cfg         Config
	mu          sync.Mutex
	threadID    string
	pending     *protocol.Block
	interaction InteractionHandler
}

func New(m api.Manager, cfg Config) *Runtime {
	if cfg.SessionID == "" {
		cfg.SessionID = protocol.NewID("session")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 250 * time.Millisecond
	}
	life, detach := context.WithCancel(context.Background())
	return &Runtime{manager: m, cfg: cfg, threadID: cfg.ThreadID, lifetime: life, detach: detach}
}
func (r *Runtime) SessionID() string { return r.cfg.SessionID }
func (r *Runtime) ThreadID() string  { r.mu.Lock(); defer r.mu.Unlock(); return r.threadID }
func (r *Runtime) Name() string {
	if r.cfg.Name != "" {
		return r.cfg.Name
	}
	return "DeepAgent Worker"
}
func (r *Runtime) ClearHistory() { r.mu.Lock(); defer r.mu.Unlock(); r.threadID = ""; r.pending = nil }
func (r *Runtime) ensureThread(ctx context.Context) (api.Thread, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.threadID != "" {
		return r.manager.GetThread(ctx, r.threadID)
	}
	t, err := r.manager.CreateThread(ctx, api.CreateThreadRequest{SessionID: r.cfg.SessionID, WorkDir: r.cfg.WorkDir, PlanMode: r.cfg.PlanMode})
	if err == nil {
		r.threadID = t.ID
	}
	return t, err
}
func (r *Runtime) SetPlanMode(ctx context.Context, on bool) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.threadID != "" {
		if err := r.manager.SetPlanMode(ctx, r.threadID, on); err != nil {
			return r.cfg.PlanMode, err
		}
	}
	r.cfg.PlanMode = on
	return on, nil
}
func (r *Runtime) StartRun(ctx context.Context, input protocol.Input) (*RunStream, error) {
	if input.Kind == "" {
		input.Kind = protocol.InputUser
	}
	if err := input.Validate(); err != nil {
		return nil, err
	}
	t, err := r.ensureThread(ctx)
	if err != nil {
		return nil, err
	}
	subCtx, cancelContext := context.WithCancel(ctx)
	stop := context.AfterFunc(r.lifetime, cancelContext)
	cancel := func() { stop(); cancelContext() }
	sub, err := r.manager.SubscribeSession(subCtx, t.SessionID)
	if err != nil {
		cancel()
		return nil, err
	}
	fail := func(err error) (*RunStream, error) {
		cancel()
		if sub.Close != nil {
			sub.Close()
		}
		return nil, err
	}
	// Establish a durable boundary before submission. Realtime is already subscribed.
	cursor := int64(0)
	for {
		rows, e := r.manager.ListEvents(ctx, api.EventFilter{ThreadID: t.ID, After: cursor, Limit: 500})
		if e != nil {
			return fail(e)
		}
		for _, ev := range rows {
			if ev.Sequence > cursor {
				cursor = ev.Sequence
			}
		}
		if len(rows) < 500 {
			break
		}
	}
	if input.ID == "" {
		input.ID = protocol.NewID("message")
	}
	if input.Kind == protocol.InputResume {
		input, err = r.resumeWhenBlocked(ctx, t.ID, input)
	} else {
		input, err = r.manager.SubmitInput(ctx, t.ID, input)
	}
	if err != nil {
		return fail(err)
	}
	s := &RunStream{inputKind: input.Kind, Events: make(chan protocol.Event, 64), manager: r.manager, threadID: t.ID, messageID: input.ID, cancel: cancel, sub: sub}
	if input.Resume != nil {
		s.runID = input.Resume.RunID
	}
	go s.consume(subCtx, cursor, r.cfg.PollInterval)
	return s, nil
}
func (r *Runtime) resumeWhenBlocked(ctx context.Context, id string, input protocol.Input) (protocol.Input, error) {
	// The persisted blocked event can precede the Worker's release. Wait for that exact block.
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		t, err := r.manager.GetThread(ctx, id)
		if err != nil {
			return input, err
		}
		if t.State == api.Blocked {
			return r.manager.ResumeFromBlock(ctx, id, input)
		}
		if t.State != api.Running {
			return input, fmt.Errorf("thread cannot resume from %s", t.State)
		}
		select {
		case <-ctx.Done():
			return input, ctx.Err()
		case <-ticker.C:
		}
	}
}
func (r *Runtime) Cancel(ctx context.Context) error {
	id := r.ThreadID()
	if id == "" {
		return nil
	}
	return r.manager.Cancel(ctx, id, "")
}
func (r *Runtime) CloseThread(ctx context.Context) error {
	id := r.ThreadID()
	if id == "" {
		return nil
	}
	return r.manager.RequestThreadClose(ctx, id)
}
func (r *Runtime) History(ctx context.Context) ([]protocol.Event, error) {
	id := r.ThreadID()
	if id == "" {
		return nil, nil
	}
	var out []protocol.Event
	var cursor int64
	for {
		rows, err := r.manager.ListEvents(ctx, api.EventFilter{ThreadID: id, After: cursor, Limit: 500})
		if err != nil {
			return nil, err
		}
		out = append(out, rows...)
		for _, e := range rows {
			if e.Sequence > cursor {
				cursor = e.Sequence
			}
		}
		if len(rows) < 500 {
			return out, nil
		}
	}
}

// Detach releases client subscriptions without cancelling or closing server work.
func (r *Runtime) Detach() { r.detach() }
