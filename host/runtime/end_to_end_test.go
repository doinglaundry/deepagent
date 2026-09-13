package runtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"eino-cli/backend/modelhub"
	host "eino-cli/host/runtime"
	"eino-cli/manager"
	"eino-cli/manager/api"
	"eino-cli/protocol"
	"eino-cli/worker/distributed"
	"eino-cli/worker/managed"
)

func testManager(t *testing.T) api.Manager {
	t.Helper()
	dsn := os.Getenv("DEEPAGENT_TEST_MYSQL_DSN")
	if dsn == "" {
		return manager.NewMemory(protocol.NewID("test"))
	}
	m, err := manager.New(context.Background(), manager.Config{Namespace: protocol.NewID("e2e"), MySQLDSN: dsn, RedisAddr: os.Getenv("DEEPAGENT_TEST_REDIS_ADDR")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}
func fakeModelServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	firstCrash := &atomic.Bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			http.Error(w, "bad JSON", 400)
			return
		}
		wantCrash := false
		wantWrite := false
		toolDone := false
		for _, m := range req.Messages {
			if m.Role == "user" && strings.Contains(fmt.Sprint(m.Content), "crash fixture") {
				wantCrash = true
			}
			if m.Role == "user" && strings.Contains(fmt.Sprint(m.Content), "write fixture") {
				wantWrite = true
			}
			if m.Role == "tool" {
				toolDone = true
			}
		}
		if wantCrash && firstCrash.CompareAndSwap(false, true) {
			select {
			case <-r.Context().Done():
			case <-time.After(20 * time.Second):
			}
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		chunk := func(delta any, finish any) {
			data, _ := json.Marshal(map[string]any{"id": "response", "object": "chat.completion.chunk", "model": "fake", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
			fmt.Fprintf(w, "data: %s\n\n", data)
			w.(http.Flusher).Flush()
		}
		if wantWrite && !toolDone {
			chunk(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "write-one", "type": "function", "function": map[string]any{"name": "write_file", "arguments": `{"path":"fixture.txt","content":"approved content"}`}}}}, nil)
			chunk(map[string]any{}, "tool_calls")
		} else {
			chunk(map[string]any{"role": "assistant", "content": "hel"}, nil)
			chunk(map[string]any{"content": "lo"}, nil)
			chunk(map[string]any{}, "stop")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(server.Close)
	return server, calls
}
func startWorker(t *testing.T, m api.Manager, url string) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cfg := distributed.Config{Manager: manager.Config{Namespace: "test", MySQLDSN: "injected", RedisAddr: "injected"}, DefaultModel: "test", Models: []modelhub.Config{{Name: "test", Provider: "openai", Model: "fake", BaseURL: url + "/v1", APIKey: "test-only"}}, MaxSteps: 20, MaxModelCalls: 5}
	factory, cleanup, err := distributed.NewFactory(ctx, m, cfg)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	worker, err := managed.New(m, factory, managed.Config{Concurrency: 2, PermitTTL: 3 * time.Second, PollInterval: 10 * time.Millisecond, ShutdownGrace: time.Second})
	if err != nil {
		cancel()
		cleanup()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()
	var stopped atomic.Bool
	stop := func() {
		if stopped.Swap(true) {
			return
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(6 * time.Second):
			t.Error("worker did not drain shutdown")
		}
		cleanup()
	}
	t.Cleanup(stop)
	return stop
}
func TestManagedConversationTwoWorkersAndSharedHistory(t *testing.T) {
	m := testManager(t)
	server, calls := fakeModelServer(t)
	startWorker(t, m, server.URL)
	startWorker(t, m, server.URL)
	r := host.New(m, host.Config{SessionID: protocol.NewID("s"), WorkDir: t.TempDir(), PollInterval: 5 * time.Millisecond})
	defer r.Detach()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var deltas strings.Builder
	result, err := r.ExecuteEvents(ctx, "hello", func(e protocol.Event) {
		if e.Kind == protocol.EventTextDelta {
			deltas.WriteString(e.Text)
		}
	})
	if err != nil || !result.Success || result.Output != "hello" {
		t.Fatalf("result %+v err %v", result, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("competing workers duplicated model execution: %d", calls.Load())
	}
	history, err := m.LoadHistory(ctx, r.ThreadID())
	if err != nil || !strings.Contains(string(history.Messages), "hello") {
		t.Fatalf("shared history missing: %+v %v", history, err)
	}
	events, err := r.History(ctx)
	if err != nil {
		t.Fatal(err)
	}
	terminal := 0
	for _, e := range events {
		if e.Kind == protocol.EventRunCompleted {
			terminal++
		}
	}
	if terminal != 1 {
		t.Fatalf("expected one durable completion, got %d", terminal)
	}
}
func TestApprovalResumesOnNewWorkerWithoutRepeatingModelDecision(t *testing.T) {
	m := testManager(t)
	server, calls := fakeModelServer(t)
	stop := startWorker(t, m, server.URL)
	dir := t.TempDir()
	r := host.New(m, host.Config{SessionID: protocol.NewID("s"), WorkDir: dir, PollInterval: 5 * time.Millisecond})
	defer r.Detach()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var blocked *protocol.Block
	result, err := r.ExecuteEvents(ctx, "write fixture", func(e protocol.Event) {
		if e.Kind == protocol.EventBlocked {
			blocked = e.Block
		}
	})
	if err != nil || !result.NeedsUser || blocked == nil {
		t.Fatalf("expected block: %+v %v block %+v", result, err, blocked)
	}
	if _, err = os.Stat(filepath.Join(dir, "fixture.txt")); !os.IsNotExist(err) {
		t.Fatal("tool ran before approval")
	}
	stop()
	startWorker(t, m, server.URL)
	resumed := host.New(m, host.Config{SessionID: r.SessionID(), ThreadID: r.ThreadID(), WorkDir: dir, PollInterval: 5 * time.Millisecond})
	defer resumed.Detach()
	var completedRun string
	result, err = resumed.ExecuteEvents(ctx, "yes", func(e protocol.Event) {
		if e.Kind == protocol.EventRunCompleted {
			completedRun = e.RunID
		}
	})
	if err != nil || !result.Success {
		t.Fatalf("resume: %+v %v", result, err)
	}
	if completedRun != blocked.RunID {
		t.Fatalf("resume created unrelated run: %s != %s", completedRun, blocked.RunID)
	}
	data, err := os.ReadFile(filepath.Join(dir, "fixture.txt"))
	if err != nil || string(data) != "approved content" {
		t.Fatalf("write result %q %v", data, err)
	}
	if calls.Load() != 2 {
		t.Fatalf("repeated pre-approval model step: %d", calls.Load())
	}
}
