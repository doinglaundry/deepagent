package runtime_test

import (
	"bytes"
	"context"
	"eino-cli/deepagent/protocol"
	eventpkg "eino-cli/deepagent/protocol/event"
	inputpkg "eino-cli/deepagent/protocol/input"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Real entrypoints and independent OS processes, with a local fake model only.
// Binaries are built by scripts/test-distributed.sh when infrastructure is supplied.
func TestWebAndIndependentWorkerProcesses(t *testing.T) {
	webBin, workerBin := os.Getenv("DEEPAGENT_TEST_WEB"), os.Getenv("DEEPAGENT_TEST_WORKER")
	dsn, redis := os.Getenv("DEEPAGENT_TEST_MYSQL_DSN"), os.Getenv("DEEPAGENT_TEST_REDIS_ADDR")
	if webBin == "" || workerBin == "" || dsn == "" || redis == "" {
		t.Skip("set dedicated DB/Redis and DEEPAGENT_TEST_WEB/WORKER binaries")
	}
	server, calls := fakeModelServer(t)
	dir := t.TempDir()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		logs, _ := filepath.Glob(filepath.Join(dir, "*.log"))
		for _, path := range logs {
			data, err := os.ReadFile(path)
			if err == nil {
				t.Logf("%s:\n%s", filepath.Base(path), data)
			}
		}
	})
	cfg := filepath.Join(dir, "config.yaml")
	wire := fmt.Sprintf("manager:\n  mysql_dsn: %q\n  redis_addr: %q\nworker:\n  concurrency: 2\n  lease_ms: 3000\n  scan_interval: 20ms\n  renew_interval: 500ms\n  message_poll_interval: 20ms\n  idle_timeout: 100ms\n  shutdown_drain_timeout: 1s\n  shutdown_interrupt_drain_timeout: 1s\ndefault_model: fake\nmodels:\n  - name: fake\n    provider: openai\n    model: fake\n    base_url: %q\n    api_key: test-only\n", dsn, redis, server.URL+"/v1")
	if err := os.WriteFile(cfg, []byte(wire), 0600); err != nil {
		t.Fatal(err)
	}
	var active *exec.Cmd
	start := func() func() {
		log, err := os.CreateTemp(dir, "worker-*.log")
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(workerBin, "--config", cfg)
		cmd.Stdout = log
		cmd.Stderr = log
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		active = cmd
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		stopped := false
		stop := func() {
			if stopped {
				return
			}
			stopped = true
			_ = cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("worker process did not exit gracefully")
			}
			_ = log.Close()
		}
		t.Cleanup(stop)
		return stop
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	webLog, err := os.CreateTemp(dir, "web-*.log")
	if err != nil {
		t.Fatal(err)
	}
	webCommand := exec.Command(webBin, "--config", cfg, "--root", dir, "--addr", address)
	webCommand.Stdout, webCommand.Stderr = webLog, webLog
	if err := webCommand.Start(); err != nil {
		t.Fatal(err)
	}
	webDone := make(chan error, 1)
	go func() { webDone <- webCommand.Wait() }()
	t.Cleanup(func() {
		_ = webCommand.Process.Signal(syscall.SIGTERM)
		select {
		case <-webDone:
		case <-time.After(5 * time.Second):
			_ = webCommand.Process.Kill()
			<-webDone
			t.Error("Web process did not exit gracefully")
		}
		webLog.Close()
	})
	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://" + address
	ready := false
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		response, err := client.Get(baseURL + "/")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("Web process did not become ready; inspect " + webLog.Name())
	}
	post := func(ctx context.Context, path string, body any) ([]byte, error) {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(raw))
		if err != nil {
			return nil, err
		}
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if response.StatusCode >= 300 {
			return nil, fmt.Errorf("HTTP %d: %s", response.StatusCode, data)
		}
		return data, err
	}
	createThread := func(ctx context.Context) (string, error) {
		raw, err := post(ctx, "/api/threads", map[string]string{"session_id": protocol.NewID("web-session"), "work_dir": dir})
		if err != nil {
			return "", err
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &created); err != nil {
			return "", err
		}
		return created.ID, nil
	}
	readEvents := func(ctx context.Context, threadID string) ([]protocol.Event, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/threads/"+threadID+"/events", nil)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("events HTTP %d", response.StatusCode)
		}
		var rows []struct {
			Sequence string          `json:"sequence"`
			RunID    string          `json:"run_id"`
			Kind     string          `json:"kind"`
			Status   string          `json:"status"`
			Payload  json.RawMessage `json:"payload"`
		}
		if err := json.NewDecoder(response.Body).Decode(&rows); err != nil {
			return nil, err
		}
		var events []protocol.Event
		for _, row := range rows {
			event := protocol.Event{ID: row.Sequence, ThreadID: threadID, RunID: row.RunID}
			switch row.Kind {
			case "input":
				if row.RunID == "" {
					continue
				}
				event.ID += ":" + row.RunID
				event.Kind = protocol.EventInputConsumed
				if row.Status == "completed" {
					events = append(events, event)
					event.ID += ":completed"
					event.Kind = protocol.EventRunCompleted
				}
			case "approval", "question", "interrupt":
				var payload eventpkg.ApprovalRequiredEventPayload
				if err := json.Unmarshal(row.Payload, &payload); err != nil {
					return nil, err
				}
				event.Kind = protocol.EventBlocked
				event.Block = &protocol.Block{RunID: row.RunID, CheckpointID: payload.CheckpointID, InterruptID: payload.InterruptID, Kind: payload.Kind, ToolName: payload.ToolName}
			case "error":
				var payload eventpkg.ErrorEventPayload
				if err := json.Unmarshal(row.Payload, &payload); err != nil {
					return nil, err
				}
				event.Kind, event.Error = protocol.EventRunFailed, payload.Message
			default:
				continue
			}
			events = append(events, event)
		}
		return events, nil
	}
	call := func(threadID, prompt string, resume *protocol.Block) ([]protocol.Event, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if threadID == "" {
			var err error
			threadID, err = createThread(ctx)
			if err != nil {
				return nil, err
			}
		}
		seen := map[string]bool{}
		previous, err := readEvents(ctx, threadID)
		if err != nil {
			return nil, err
		}
		for _, event := range previous {
			seen[event.ID] = true
		}
		body := map[string]any{"text": prompt}
		if resume != nil {
			body = map[string]any{"resume": inputpkg.ResumeRunPayload{RunID: resume.RunID, CheckpointID: resume.CheckpointID, InterruptID: resume.InterruptID, Approval: &inputpkg.ApprovalDecision{Approved: true}}}
		}
		if _, err := post(ctx, "/api/threads/"+threadID+"/messages", body); err != nil {
			return nil, err
		}
		var events []protocol.Event
		for {
			rows, err := readEvents(ctx, threadID)
			if err != nil {
				return events, err
			}
			for _, event := range rows {
				if seen[event.ID] {
					continue
				}
				seen[event.ID] = true
				events = append(events, event)
				switch event.Kind {
				case protocol.EventBlocked:
					return events, fmt.Errorf("waiting for input")
				case protocol.EventRunCompleted:
					response, err := client.Get(baseURL + "/api/threads/" + threadID + "/events")
					if err != nil {
						return events, err
					}
					defer response.Body.Close()
					var displayed []json.RawMessage
					if err := json.NewDecoder(response.Body).Decode(&displayed); err != nil {
						return events, err
					}
					if response.StatusCode != 200 || len(displayed) == 0 {
						return events, fmt.Errorf("Web history unavailable")
					}
					return events, nil
				case protocol.EventRunFailed:
					return events, fmt.Errorf("worker failed: %s", event.Error)
				}
			}
			select {
			case <-ctx.Done():
				return events, ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	stop := start()
	events, err := call("", "write fixture", nil)
	if err == nil {
		t.Fatal("one-shot blocked run should report waiting input")
	}
	var block protocol.Event
	for _, e := range events {
		if e.Kind == protocol.EventBlocked {
			block = e
		}
	}
	if block.Block == nil {
		t.Fatalf("no blocked event: %v err=%v", events, err)
	}
	blocked := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		response, getErr := client.Get(baseURL + "/api/threads/" + block.ThreadID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		var thread struct {
			Status string `json:"status"`
		}
		decodeErr := json.NewDecoder(response.Body).Decode(&thread)
		response.Body.Close()
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		if thread.Status == "blocked" {
			blocked = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("approval was persisted but Thread did not become blocked")
	}
	stop()
	stop = start()
	events, err = call(block.ThreadID, "", block.Block)
	if err != nil {
		t.Fatalf("resume process failed: %v events=%v", err, events)
	}
	completed := ""
	for _, e := range events {
		if e.Kind == protocol.EventRunCompleted {
			completed = e.RunID
		}
	}
	if completed != block.RunID {
		t.Fatalf("run association lost: %q != %q", completed, block.RunID)
	}
	content, err := os.ReadFile(filepath.Join(dir, "fixture.txt"))
	if err != nil || string(content) != "approved content" {
		t.Fatalf("file=%q error=%v", content, err)
	}
	// Kill the owning process inside a model request. The polling Web client must
	// follow the retried input under a new Run after the old permit expires.
	crashThread, err := createThread(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	before := calls.Load()
	type outcome struct {
		events []protocol.Event
		err    error
	}
	result := make(chan outcome, 1)
	go func() {
		e, err := call(crashThread, "crash fixture", nil)
		result <- outcome{e, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for calls.Load() == before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() == before {
		t.Fatal("worker never reached model before crash")
	}
	// Ensure the first Run association is durable before forcing the crash;
	// the model can start before the asynchronous output publisher catches up.
	consumed := false
	for time.Now().Before(deadline) {
		rows, e := readEvents(context.Background(), crashThread)
		if e != nil {
			t.Fatal(e)
		}
		for _, event := range rows {
			if event.Kind == protocol.EventInputConsumed {
				consumed = true
			}
		}
		if consumed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !consumed {
		t.Fatal("first Run was not persisted before crash")
	}
	if err := active.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	stop()
	start()
	recovered := <-result
	if recovered.err != nil {
		t.Fatalf("Web submission did not survive owner crash: %v events=%v", recovered.err, recovered.events)
	}
	runs := map[string]bool{}
	finished := false
	for _, e := range recovered.events {
		if e.Kind == protocol.EventInputConsumed {
			runs[e.RunID] = true
		}
		if e.Kind == protocol.EventRunCompleted {
			finished = true
		}
	}
	if len(runs) < 2 || !finished {
		t.Fatalf("expected new Run after process death: runs=%v completed=%v events=%v", runs, finished, recovered.events)
	}

}
