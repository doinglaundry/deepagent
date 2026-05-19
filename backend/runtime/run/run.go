package run

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"eino-cli/backend/agent/middlewares"
	rt "eino-cli/backend/runtime"
	runtimecontext "eino-cli/backend/runtime/context"
	"eino-cli/backend/session/rollback"
	"eino-cli/backend/session/runs"
)

type Status string

const (
	Pending     Status = "pending"
	Running     Status = "running"
	Success     Status = "success"
	Error       Status = "error"
	Interrupted Status = "interrupted"
)

type Record struct {
	ID            string
	Prompt        string
	Status        Status
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Cancel        context.CancelFunc
	Output        string
	Err           error
	TotalTokens   int64
	Rollbackable  bool
	RollbackPath  string
	RollbackError string
}

type Manager struct {
	mu            sync.Mutex
	current       *Record
	store         *runs.Store
	rollbackStore *rollback.Store
}

func NewManager() *Manager {
	return &Manager{}
}

func NewManagerWithStore(store *runs.Store, rollbackStores ...*rollback.Store) *Manager {
	var rollbackStore *rollback.Store
	if len(rollbackStores) > 0 {
		rollbackStore = rollbackStores[0]
	}
	return &Manager{store: store, rollbackStore: rollbackStore}
}

func (m *Manager) ListRuns(ctx context.Context) ([]runs.Record, error) {
	m.mu.Lock()
	store := m.store
	m.mu.Unlock()
	if store == nil {
		return nil, nil
	}
	return store.List(ctx)
}

func (m *Manager) RestoreSnapshot(ctx context.Context, runID string) ([]byte, error) {
	m.mu.Lock()
	store := m.rollbackStore
	m.mu.Unlock()
	if store == nil {
		return nil, fmt.Errorf("rollback store is not configured")
	}
	return store.RestorePost(ctx, runID)
}

type EventType string

const (
	EventMetadata EventType = "metadata"
	EventChunk    EventType = "chunk"
	EventTrace    EventType = "trace"
	EventDone     EventType = "done"
	EventError    EventType = "error"
	EventEnd      EventType = "end"
)

type Event struct {
	RunID  string
	Type   EventType
	Chunk  string
	Trace  *middlewares.TraceEvent
	Output string
	Err    error
}

type eventBridge struct {
	ch chan Event
}

func Start(ctx context.Context, runtime rt.Runtime, prompt string, mgr *Manager) (<-chan Event, context.CancelFunc, error) {
	if mgr == nil {
		mgr = NewManager()
	}
	run, runCtx, err := create(ctx, mgr, prompt)
	if err != nil {
		return nil, nil, err
	}
	bridge := &eventBridge{ch: make(chan Event, 64)}
	go startWorker(runCtx, runtime, prompt, mgr, run, bridge)
	return bridge.ch, run.Cancel, nil
}

func create(ctx context.Context, mgr *Manager, prompt string) (*Record, context.Context, error) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	if mgr.current != nil && (mgr.current.Status == Pending || mgr.current.Status == Running) {
		return nil, nil, fmt.Errorf("run already in progress")
	}

	runCtx, cancel := context.WithCancel(ctx)
	now := time.Now()
	run := &Record{
		ID:        fmt.Sprintf("run-%d", now.UnixNano()),
		Prompt:    prompt,
		Status:    Pending,
		CreatedAt: now,
		UpdatedAt: now,
		Cancel:    cancel,
	}
	mgr.current = run
	return run, runCtx, nil
}

func finish(mgr *Manager, run *Record, status Status, output string, err error) {
	mgr.mu.Lock()
	run.Status = status
	run.Output = output
	run.Err = err
	run.UpdatedAt = time.Now()
	store := mgr.store
	snapshot := toRecord(run)
	mgr.mu.Unlock()

	if store == nil {
		return
	}
	if saveErr := store.Save(context.Background(), snapshot); saveErr != nil {
		slog.Warn("run store: save failed", "run_id", run.ID, "err", saveErr)
	}
}

func toRecord(run *Record) runs.Record {
	rec := runs.Record{
		ID:            run.ID,
		Status:        string(run.Status),
		Prompt:        run.Prompt,
		CreatedAt:     run.CreatedAt,
		UpdatedAt:     run.UpdatedAt,
		DurationMS:    run.UpdatedAt.Sub(run.CreatedAt).Milliseconds(),
		Output:        run.Output,
		Tokens:        run.TotalTokens,
		Rollbackable:  run.Rollbackable,
		RollbackPath:  run.RollbackPath,
		RollbackError: run.RollbackError,
	}
	if run.Err != nil {
		rec.Error = run.Err.Error()
	}
	return rec
}

func capturePostSnapshot(ctx context.Context, runtime rt.Runtime, mgr *Manager, run *Record, policyState *runtimecontext.RollbackPolicyState) {
	mgr.mu.Lock()
	store := mgr.store
	rollbackStore := mgr.rollbackStore
	mgr.mu.Unlock()
	if store == nil || rollbackStore == nil {
		return
	}
	if runtimecontext.WasRollbackUnsafeToolBlocked(policyState) {
		markRollback(mgr, run, "", "unsafe shell/execute tool was blocked")
		_ = store.Save(context.Background(), toRecord(run))
		return
	}
	history, err := runtime.ExportHistory()
	if err != nil {
		markRollback(mgr, run, "", fmt.Sprintf("export history: %v", err))
		_ = store.Save(context.Background(), toRecord(run))
		return
	}
	path, err := rollbackStore.SavePost(ctx, run.ID, history)
	if err != nil {
		markRollback(mgr, run, "", err.Error())
		_ = store.Save(context.Background(), toRecord(run))
		return
	}
	markRollback(mgr, run, path, "")
	rec := toRecord(run)
	if err := store.Save(context.Background(), rec); err != nil {
		slog.Warn("run store: save rollback metadata failed", "run_id", run.ID, "err", err)
	}
	if err := runs.NewStore(filepath.Join(path, "runs")).Save(context.Background(), rec); err != nil {
		slog.Warn("rollback snapshot: save run metadata failed", "run_id", run.ID, "err", err)
	}
}

func markRollback(mgr *Manager, run *Record, path, detail string) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	run.RollbackPath = path
	run.Rollbackable = path != "" && detail == ""
	run.RollbackError = detail
}

func publish(ctx context.Context, bridge *eventBridge, ev Event) bool {
	select {
	case bridge.ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

type traceConsumer struct {
	ctx    context.Context
	mgr    *Manager
	run    *Record
	bridge *eventBridge
}

func (c traceConsumer) Send(ev middlewares.TraceEvent) {
	publish(c.ctx, c.bridge, Event{
		RunID: c.run.ID,
		Type:  EventTrace,
		Trace: &ev,
	})
	if ev.Phase == middlewares.TracePhaseTokens && ev.Tokens != nil {
		c.mgr.mu.Lock()
		c.run.TotalTokens = ev.Tokens.TotalTokens
		c.mgr.mu.Unlock()
	}
}

func startWorker(ctx context.Context, runtime rt.Runtime, prompt string, mgr *Manager, run *Record, bridge *eventBridge) {
	defer close(bridge.ch)

	mgr.mu.Lock()
	run.Status = Running
	run.UpdatedAt = time.Now()
	rollbackProtected := mgr.rollbackStore != nil
	mgr.mu.Unlock()
	var policyState *runtimecontext.RollbackPolicyState
	if rollbackProtected {
		policyState = &runtimecontext.RollbackPolicyState{}
		ctx = runtimecontext.WithRollbackProtected(ctx, true)
		ctx = runtimecontext.WithRollbackPolicyState(ctx, policyState)
	}
	ctx = middlewares.WithTraceConsumer(ctx, traceConsumer{
		ctx:    ctx,
		mgr:    mgr,
		run:    run,
		bridge: bridge,
	})
	publish(ctx, bridge, Event{RunID: run.ID, Type: EventMetadata})

	result, err := runtime.ExecuteStream(ctx, prompt, func(chunk string) {
		publish(ctx, bridge, Event{RunID: run.ID, Type: EventChunk, Chunk: chunk})
	})
	if err != nil {
		finish(mgr, run, Error, "", err)
		capturePostSnapshot(context.Background(), runtime, mgr, run, policyState)
		publish(context.Background(), bridge, Event{RunID: run.ID, Type: EventError, Err: err})
		publish(context.Background(), bridge, Event{RunID: run.ID, Type: EventEnd})
		return
	}
	if ctx.Err() != nil || result.NeedsUser {
		err := ctx.Err()
		finish(mgr, run, Interrupted, result.Output, err)
		capturePostSnapshot(context.Background(), runtime, mgr, run, policyState)
		publish(context.Background(), bridge, Event{RunID: run.ID, Type: EventDone, Output: result.Output})
		publish(context.Background(), bridge, Event{RunID: run.ID, Type: EventEnd})
		return
	}

	finish(mgr, run, Success, result.Output, nil)
	capturePostSnapshot(context.Background(), runtime, mgr, run, policyState)
	publish(context.Background(), bridge, Event{RunID: run.ID, Type: EventDone, Output: result.Output})
	publish(context.Background(), bridge, Event{RunID: run.ID, Type: EventEnd})
}
