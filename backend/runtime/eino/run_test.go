package eino

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/session/rollback"
	"eino-cli/backend/session/runs"
)

type runTestRuntime struct {
	run func(ctx context.Context, prompt string, onChunk StreamChunkHandler) (Result, error)
}

func (r runTestRuntime) ExecuteStream(ctx context.Context, prompt string, onChunk StreamChunkHandler) (Result, error) {
	return r.run(ctx, prompt, onChunk)
}

func (r runTestRuntime) ClearHistory() {}

func (r runTestRuntime) ExportHistory() ([]byte, error) { return []byte("[]"), nil }

func (r runTestRuntime) ImportHistory([]byte) error { return nil }

func (r runTestRuntime) RollbackToHistory([]byte) error { return nil }

func (r runTestRuntime) SetPlanMode(context.Context, bool) (bool, error) { return false, nil }

func (r runTestRuntime) Name() string { return "test" }

func TestStartRunPublishesOrderedEvents(t *testing.T) {
	rt := runTestRuntime{run: func(_ context.Context, _ string, onChunk StreamChunkHandler) (Result, error) {
		onChunk("hello")
		return SuccessResult("hello"), nil
	}}
	mgr := NewRunManager()

	events, _, err := StartRun(context.Background(), rt, "hi", mgr)
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}

	var got []RunEventType
	var output string
	for ev := range events {
		got = append(got, ev.Type)
		if ev.Type == RunEventDone {
			output = ev.Output
		}
	}

	want := []RunEventType{RunEventMetadata, RunEventChunk, RunEventDone, RunEventEnd}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if output != "hello" {
		t.Fatalf("output = %q, want hello", output)
	}
	if mgr.current == nil || mgr.current.Status != RunSuccess {
		t.Fatalf("run status = %#v, want success", mgr.current)
	}
}

func TestStartRunPublishesErrorThenEnd(t *testing.T) {
	wantErr := errors.New("boom")
	rt := runTestRuntime{run: func(context.Context, string, StreamChunkHandler) (Result, error) {
		return Result{}, wantErr
	}}
	mgr := NewRunManager()

	events, _, err := StartRun(context.Background(), rt, "hi", mgr)
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}

	var got []RunEventType
	var runErr error
	for ev := range events {
		got = append(got, ev.Type)
		if ev.Type == RunEventError {
			runErr = ev.Err
		}
	}

	want := []RunEventType{RunEventMetadata, RunEventError, RunEventEnd}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if !errors.Is(runErr, wantErr) {
		t.Fatalf("error = %v, want %v", runErr, wantErr)
	}
	if mgr.current == nil || mgr.current.Status != RunError {
		t.Fatalf("run status = %#v, want error", mgr.current)
	}
}

func TestStartRunPersistsRecord(t *testing.T) {
	dir := t.TempDir()
	store := runs.NewStore(dir)
	mgr := NewRunManagerWithStore(store)
	rt := runTestRuntime{run: func(context.Context, string, StreamChunkHandler) (Result, error) {
		return SuccessResult("answer"), nil
	}}

	events, _, err := StartRun(context.Background(), rt, "ask me", mgr)
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	for range events {
	}

	listed, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("List() len = %d, want 1", len(listed))
	}
	got := listed[0]
	if got.Status != "success" {
		t.Fatalf("Status = %q, want success", got.Status)
	}
	if got.Prompt != "ask me" {
		t.Fatalf("Prompt = %q, want ask me", got.Prompt)
	}
	if got.Output != "answer" {
		t.Fatalf("Output = %q, want answer", got.Output)
	}
	if got.DurationMS < 0 {
		t.Fatalf("DurationMS = %d, want >= 0", got.DurationMS)
	}

	files, _ := os.ReadDir(dir)
	for _, f := range files {
		if filepath.Ext(f.Name()) != ".json" {
			continue
		}
		payload, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}
		var rec runs.Record
		if err := json.Unmarshal(payload, &rec); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if rec.ID != got.ID {
			t.Fatalf("on-disk ID = %q, want %q", rec.ID, got.ID)
		}
	}
}

func TestStartRunPersistsTotalTokens(t *testing.T) {
	dir := t.TempDir()
	store := runs.NewStore(dir)
	mgr := NewRunManagerWithStore(store)
	rt := runTestRuntime{run: func(ctx context.Context, _ string, _ StreamChunkHandler) (Result, error) {
		consumer := middlewares.GetTraceConsumer(ctx)
		if consumer == nil {
			t.Fatal("trace consumer missing from ctx")
		}
		consumer.Send(middlewares.TraceEvent{
			Phase:  middlewares.TracePhaseTokens,
			Tokens: &middlewares.TokenUsageStats{TotalTokens: 1234},
		})
		return SuccessResult("done"), nil
	}}

	events, _, err := StartRun(context.Background(), rt, "hi", mgr)
	if err != nil {
		t.Fatalf("StartRun() error = %v", err)
	}
	for range events {
	}

	listed, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("List() len = %d, want 1", len(listed))
	}
	if listed[0].Tokens != 1234 {
		t.Fatalf("Tokens = %d, want 1234", listed[0].Tokens)
	}
}

func TestStartRunCapturesRollbackSnapshot(t *testing.T) {
	root := t.TempDir()
	runStore := runs.NewStore(filepath.Join(root, ".eino-cli", "runs"))
	rt := runTestRuntime{run: func(ctx context.Context, _ string, _ StreamChunkHandler) (Result, error) {
		if !middlewares.IsRollbackProtected(ctx) {
			t.Fatal("run context should be rollback protected")
		}
		return SuccessResult("answer"), nil
	}}
	mgr := NewRunManagerWithStore(runStore, rollback.NewStore(root))

	events, _, err := StartRun(context.Background(), rt, "prompt", mgr)
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}

	records, err := runStore.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records len = %d, want 1", len(records))
	}
	if !records[0].Rollbackable {
		t.Fatalf("record should be rollbackable: %#v", records[0])
	}
	if records[0].RollbackPath == "" {
		t.Fatal("rollback path should be set")
	}
	if _, err := os.Stat(filepath.Join(records[0].RollbackPath, "snapshot.json")); err != nil {
		t.Fatal(err)
	}
}

func TestStartRunDoesNotRollbackAfterUnsafeToolBlocked(t *testing.T) {
	root := t.TempDir()
	runStore := runs.NewStore(filepath.Join(root, ".eino-cli", "runs"))
	rt := runTestRuntime{run: func(ctx context.Context, _ string, _ StreamChunkHandler) (Result, error) {
		middlewares.MarkRollbackUnsafeToolBlocked(ctx)
		return SuccessResult("answer"), nil
	}}
	mgr := NewRunManagerWithStore(runStore, rollback.NewStore(root))

	events, _, err := StartRun(context.Background(), rt, "prompt", mgr)
	if err != nil {
		t.Fatal(err)
	}
	for range events {
	}

	records, err := runStore.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records len = %d, want 1", len(records))
	}
	if records[0].Rollbackable {
		t.Fatalf("record should not be rollbackable: %#v", records[0])
	}
	if !strings.Contains(records[0].RollbackError, "unsafe shell/execute") {
		t.Fatalf("rollback error = %q", records[0].RollbackError)
	}
}

func TestStartRunRejectsInFlightRun(t *testing.T) {
	release := make(chan struct{})
	rt := runTestRuntime{run: func(ctx context.Context, _ string, _ StreamChunkHandler) (Result, error) {
		select {
		case <-release:
			return SuccessResult("done"), nil
		case <-ctx.Done():
			return Result{NeedsUser: true}, nil
		}
	}}
	mgr := NewRunManager()

	events, cancel, err := StartRun(context.Background(), rt, "first", mgr)
	if err != nil {
		t.Fatalf("StartRun(first) error = %v", err)
	}
	if _, _, err := StartRun(context.Background(), rt, "second", mgr); err == nil {
		t.Fatal("expected in-flight run rejection")
	}

	cancel()
	for range events {
	}
	close(release)
}
