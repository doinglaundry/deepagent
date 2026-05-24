package run

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
	"eino-cli/backend/config"
	rt "eino-cli/backend/runtime"
	runtimecontext "eino-cli/backend/runtime/context"
	"eino-cli/backend/session/rollback"
	"eino-cli/backend/session/runs"
)

type testRuntime struct {
	run func(ctx context.Context, prompt string, onChunk rt.StreamChunkHandler) (rt.Result, error)
}

func (r testRuntime) ExecuteStream(ctx context.Context, prompt string, onChunk rt.StreamChunkHandler) (rt.Result, error) {
	return r.run(ctx, prompt, onChunk)
}

func (r testRuntime) RunDream(context.Context) (rt.Result, error) {
	return rt.Result{Success: true}, nil
}

func (r testRuntime) ClearHistory() {}

func (r testRuntime) ExportHistory() ([]byte, error) { return []byte("[]"), nil }

func (r testRuntime) ImportHistory([]byte) error { return nil }

func (r testRuntime) RollbackToHistory([]byte) error { return nil }

func (r testRuntime) SetPlanMode(context.Context, bool) (bool, error) { return false, nil }

func (r testRuntime) Name() string { return "test" }

func TestStartPublishesOrderedEvents(t *testing.T) {
	runtime := testRuntime{run: func(_ context.Context, _ string, onChunk rt.StreamChunkHandler) (rt.Result, error) {
		onChunk("hello")
		return rt.Result{Success: true, Output: "hello"}, nil
	}}
	mgr := NewManagerWithStore(nil)

	events, _, err := Start(context.Background(), runtime, "hi", mgr)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	var got []EventType
	var output string
	for ev := range events {
		got = append(got, ev.Type)
		if ev.Type == EventDone {
			output = ev.Output
		}
	}

	want := []EventType{EventMetadata, EventChunk, EventDone, EventEnd}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if output != "hello" {
		t.Fatalf("output = %q, want hello", output)
	}
	if mgr.current == nil || mgr.current.Status != Success {
		t.Fatalf("run status = %#v, want success", mgr.current)
	}
}

func TestStartPublishesErrorThenEnd(t *testing.T) {
	wantErr := errors.New("boom")
	runtime := testRuntime{run: func(context.Context, string, rt.StreamChunkHandler) (rt.Result, error) {
		return rt.Result{}, wantErr
	}}
	mgr := NewManagerWithStore(nil)

	events, _, err := Start(context.Background(), runtime, "hi", mgr)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	var got []EventType
	var runErr error
	for ev := range events {
		got = append(got, ev.Type)
		if ev.Type == EventError {
			runErr = ev.Err
		}
	}

	want := []EventType{EventMetadata, EventError, EventEnd}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	if !errors.Is(runErr, wantErr) {
		t.Fatalf("error = %v, want %v", runErr, wantErr)
	}
	if mgr.current == nil || mgr.current.Status != Error {
		t.Fatalf("run status = %#v, want error", mgr.current)
	}
}

func TestStartPersistsRecord(t *testing.T) {
	dir := t.TempDir()
	store := runs.NewStore(dir)
	mgr := NewManagerWithStore(store)
	runtime := testRuntime{run: func(context.Context, string, rt.StreamChunkHandler) (rt.Result, error) {
		return rt.Result{Success: true, Output: "answer"}, nil
	}}

	events, _, err := Start(context.Background(), runtime, "ask me", mgr)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
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

func TestStartPersistsTotalTokens(t *testing.T) {
	dir := t.TempDir()
	store := runs.NewStore(dir)
	mgr := NewManagerWithStore(store)
	runtime := testRuntime{run: func(ctx context.Context, _ string, _ rt.StreamChunkHandler) (rt.Result, error) {
		consumer := middlewares.GetTraceConsumer(ctx)
		if consumer == nil {
			t.Fatal("trace consumer missing from ctx")
		}
		consumer.Send(middlewares.TraceEvent{
			Phase:  middlewares.TracePhaseTokens,
			Tokens: &middlewares.TokenUsageStats{TotalTokens: 1234},
		})
		return rt.Result{Success: true, Output: "done"}, nil
	}}

	events, _, err := Start(context.Background(), runtime, "hi", mgr)
	if err != nil {
		t.Fatalf("Start() error = %v", err)
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

func TestStartCapturesRollbackSnapshot(t *testing.T) {
	root := t.TempDir()
	cleanup := config.SetRootDirForTest(root)
	defer cleanup()
	runStore := runs.NewStore(config.RunsDir())
	runtime := testRuntime{run: func(ctx context.Context, _ string, _ rt.StreamChunkHandler) (rt.Result, error) {
		if !runtimecontext.IsRollbackProtected(ctx) {
			t.Fatal("run context should be rollback protected")
		}
		return rt.Result{Success: true, Output: "answer"}, nil
	}}
	mgr := NewManagerWithStore(runStore, rollback.NewStore(root))

	events, _, err := Start(context.Background(), runtime, "prompt", mgr)
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

func TestStartDoesNotRollbackAfterUnsafeToolBlocked(t *testing.T) {
	root := t.TempDir()
	cleanup := config.SetRootDirForTest(root)
	defer cleanup()
	runStore := runs.NewStore(config.RunsDir())
	runtime := testRuntime{run: func(ctx context.Context, _ string, _ rt.StreamChunkHandler) (rt.Result, error) {
		runtimecontext.MarkRollbackUnsafeToolBlocked(ctx)
		return rt.Result{Success: true, Output: "answer"}, nil
	}}
	mgr := NewManagerWithStore(runStore, rollback.NewStore(root))

	events, _, err := Start(context.Background(), runtime, "prompt", mgr)
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

func TestStartRejectsInFlightRun(t *testing.T) {
	release := make(chan struct{})
	runtime := testRuntime{run: func(ctx context.Context, _ string, _ rt.StreamChunkHandler) (rt.Result, error) {
		select {
		case <-release:
			return rt.Result{Success: true, Output: "done"}, nil
		case <-ctx.Done():
			return rt.Result{NeedsUser: true}, nil
		}
	}}
	mgr := NewManagerWithStore(nil)

	events, cancel, err := Start(context.Background(), runtime, "first", mgr)
	if err != nil {
		t.Fatalf("Start(first) error = %v", err)
	}
	if _, _, err := Start(context.Background(), runtime, "second", mgr); err == nil {
		t.Fatal("expected in-flight run rejection")
	}

	cancel()
	for range events {
	}
	close(release)
}
