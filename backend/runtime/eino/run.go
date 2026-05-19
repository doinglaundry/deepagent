package eino

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"time"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/session/rollback"
	"eino-cli/backend/session/runs"
)

type RunStatus string

const (
	RunPending     RunStatus = "pending"
	RunRunning     RunStatus = "running"
	RunSuccess     RunStatus = "success"
	RunError       RunStatus = "error"
	RunInterrupted RunStatus = "interrupted"
)

type RunRecord struct {
	ID            string
	Prompt        string
	Status        RunStatus
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

type RunManager struct {
	mu            sync.Mutex
	current       *RunRecord
	store         *runs.Store
	rollbackStore *rollback.Store
}

func NewRunManager() *RunManager {
	return &RunManager{}
}

// NewRunManagerWithStore wires persistent run records and optional rollback snapshots.
func NewRunManagerWithStore(store *runs.Store, rollbackStores ...*rollback.Store) *RunManager {
	var rollbackStore *rollback.Store
	if len(rollbackStores) > 0 {
		rollbackStore = rollbackStores[0]
	}
	return &RunManager{store: store, rollbackStore: rollbackStore}
}

func (m *RunManager) ListRuns(ctx context.Context) ([]runs.Record, error) {
	m.mu.Lock()
	store := m.store
	m.mu.Unlock()
	if store == nil {
		return nil, nil
	}
	return store.List(ctx)
}

func (m *RunManager) RestoreSnapshot(ctx context.Context, runID string) ([]byte, error) {
	m.mu.Lock()
	store := m.rollbackStore
	m.mu.Unlock()
	if store == nil {
		return nil, fmt.Errorf("rollback store is not configured")
	}
	return store.RestorePost(ctx, runID)
}

type RunEventType string

const (
	RunEventMetadata RunEventType = "metadata"
	RunEventChunk    RunEventType = "chunk"
	RunEventTrace    RunEventType = "trace"
	RunEventDone     RunEventType = "done"
	RunEventError    RunEventType = "error"
	RunEventEnd      RunEventType = "end"
)

type RunEvent struct {
	RunID  string
	Type   RunEventType
	Chunk  string
	Trace  *middlewares.TraceEvent
	Output string
	Err    error
}

type runEventBridge struct {
	ch chan RunEvent
}

func StartRun(ctx context.Context, rt Runtime, prompt string, mgr *RunManager) (<-chan RunEvent, context.CancelFunc, error) {
	if mgr == nil {
		mgr = NewRunManager()
	}
	run, runCtx, err := createRun(ctx, mgr, prompt)
	if err != nil {
		return nil, nil, err
	}
	bridge := newRunEventBridge(64)
	go startRunWorker(runCtx, rt, prompt, mgr, run, bridge)
	return bridge.ch, run.Cancel, nil
}

func createRun(ctx context.Context, mgr *RunManager, prompt string) (*RunRecord, context.Context, error) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	if mgr.current != nil && isRunInFlight(mgr.current.Status) {
		return nil, nil, fmt.Errorf("run already in progress")
	}

	runCtx, cancel := context.WithCancel(ctx)
	now := time.Now()
	run := &RunRecord{
		ID:        fmt.Sprintf("run-%d", now.UnixNano()),
		Prompt:    prompt,
		Status:    RunPending,
		CreatedAt: now,
		UpdatedAt: now,
		Cancel:    cancel,
	}
	mgr.current = run
	return run, runCtx, nil
}

func isRunInFlight(status RunStatus) bool {
	return status == RunPending || status == RunRunning
}

// finishRun is the single status-convergence point for success / interrupted
// / error paths. Persistence rides on it so adding a new termination path
// can't forget to write the record. Save happens outside the manager lock
// so disk I/O doesn't stall concurrent reads, and failures degrade to a
// log line — the RunEventEnd protocol from phase 1 stays intact even when
// the filesystem is full.
func finishRun(mgr *RunManager, run *RunRecord, status RunStatus, output string, err error) {
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

func markRunRunning(mgr *RunManager, run *RunRecord) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()

	run.Status = RunRunning
	run.UpdatedAt = time.Now()
}

func updateRunTokens(mgr *RunManager, run *RunRecord, total int64) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	run.TotalTokens = total
}

func toRecord(run *RunRecord) runs.Record {
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

func capturePostSnapshot(ctx context.Context, rt Runtime, mgr *RunManager, run *RunRecord, policyState *middlewares.RollbackPolicyState) {
	mgr.mu.Lock()
	store := mgr.store
	rollbackStore := mgr.rollbackStore
	mgr.mu.Unlock()
	if store == nil || rollbackStore == nil {
		return
	}
	if middlewares.WasRollbackUnsafeToolBlocked(policyState) {
		markRunRollback(mgr, run, "", "unsafe shell/execute tool was blocked")
		_ = store.Save(context.Background(), toRecord(run))
		return
	}
	history, err := rt.ExportHistory()
	if err != nil {
		markRunRollback(mgr, run, "", fmt.Sprintf("export history: %v", err))
		_ = store.Save(context.Background(), toRecord(run))
		return
	}
	path, err := rollbackStore.SavePost(ctx, run.ID, history)
	if err != nil {
		markRunRollback(mgr, run, "", err.Error())
		_ = store.Save(context.Background(), toRecord(run))
		return
	}
	markRunRollback(mgr, run, path, "")
	rec := toRecord(run)
	if err := store.Save(context.Background(), rec); err != nil {
		slog.Warn("run store: save rollback metadata failed", "run_id", run.ID, "err", err)
	}
	if err := runs.NewStore(filepath.Join(path, "runs")).Save(context.Background(), rec); err != nil {
		slog.Warn("rollback snapshot: save run metadata failed", "run_id", run.ID, "err", err)
	}
}

func markRunRollback(mgr *RunManager, run *RunRecord, path, detail string) {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	run.RollbackPath = path
	run.Rollbackable = path != "" && detail == ""
	run.RollbackError = detail
}

func rollbackProtected(mgr *RunManager) bool {
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	return mgr.rollbackStore != nil
}

func newRunEventBridge(buffer int) *runEventBridge {
	return &runEventBridge{ch: make(chan RunEvent, buffer)}
}

func publishRunEvent(ctx context.Context, bridge *runEventBridge, ev RunEvent) bool {
	select {
	case bridge.ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func closeRunEvents(bridge *runEventBridge) {
	close(bridge.ch)
}

type runTraceConsumer struct {
	ctx    context.Context
	mgr    *RunManager
	run    *RunRecord
	bridge *runEventBridge
}

// Send fans every TraceEvent into the run event bridge, and additionally
// snapshots TotalTokens onto the live RunRecord when the event is a token
// usage phase. Persistence reads TotalTokens off RunRecord in finishRun.
func (c runTraceConsumer) Send(ev middlewares.TraceEvent) {
	publishRunEvent(c.ctx, c.bridge, RunEvent{
		RunID: c.run.ID,
		Type:  RunEventTrace,
		Trace: &ev,
	})
	if ev.Phase == middlewares.TracePhaseTokens && ev.Tokens != nil {
		updateRunTokens(c.mgr, c.run, ev.Tokens.TotalTokens)
	}
}

func startRunWorker(ctx context.Context, rt Runtime, prompt string, mgr *RunManager, run *RunRecord, bridge *runEventBridge) {
	defer closeRunEvents(bridge)

	markRunRunning(mgr, run)
	var policyState *middlewares.RollbackPolicyState
	if rollbackProtected(mgr) {
		policyState = &middlewares.RollbackPolicyState{}
		ctx = middlewares.WithRollbackProtected(ctx, true)
		ctx = middlewares.WithRollbackPolicyState(ctx, policyState)
	}
	ctx = middlewares.WithTraceConsumer(ctx, runTraceConsumer{
		ctx:    ctx,
		mgr:    mgr,
		run:    run,
		bridge: bridge,
	})
	publishRunEvent(ctx, bridge, RunEvent{RunID: run.ID, Type: RunEventMetadata})

	result, err := rt.ExecuteStream(ctx, prompt, func(chunk string) {
		publishRunEvent(ctx, bridge, RunEvent{RunID: run.ID, Type: RunEventChunk, Chunk: chunk})
	})
	if err != nil {
		finishRun(mgr, run, RunError, "", err)
		capturePostSnapshot(context.Background(), rt, mgr, run, policyState)
		publishRunEvent(context.Background(), bridge, RunEvent{RunID: run.ID, Type: RunEventError, Err: err})
		publishRunEvent(context.Background(), bridge, RunEvent{RunID: run.ID, Type: RunEventEnd})
		return
	}
	if ctx.Err() != nil || result.NeedsUser {
		err := ctx.Err()
		finishRun(mgr, run, RunInterrupted, result.Output, err)
		capturePostSnapshot(context.Background(), rt, mgr, run, policyState)
		publishRunEvent(context.Background(), bridge, RunEvent{RunID: run.ID, Type: RunEventDone, Output: result.Output})
		publishRunEvent(context.Background(), bridge, RunEvent{RunID: run.ID, Type: RunEventEnd})
		return
	}

	finishRun(mgr, run, RunSuccess, result.Output, nil)
	capturePostSnapshot(context.Background(), rt, mgr, run, policyState)
	publishRunEvent(context.Background(), bridge, RunEvent{RunID: run.ID, Type: RunEventDone, Output: result.Output})
	publishRunEvent(context.Background(), bridge, RunEvent{RunID: run.ID, Type: RunEventEnd})
}
