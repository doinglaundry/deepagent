package runtime_test

import (
	"bufio"
	"bytes"
	"context"
	"eino-cli/manager"
	"eino-cli/manager/api"
	"eino-cli/protocol"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Real entrypoints and independent OS processes, with a local fake model only.
// Binaries are built by scripts/test-distributed.sh when infrastructure is supplied.
func TestCLIAndIndependentWorkerProcesses(t *testing.T) {
	cliBin, workerBin := os.Getenv("DEEPAGENT_TEST_CLI"), os.Getenv("DEEPAGENT_TEST_WORKER")
	dsn, redis := os.Getenv("DEEPAGENT_TEST_MYSQL_DSN"), os.Getenv("DEEPAGENT_TEST_REDIS_ADDR")
	if cliBin == "" || workerBin == "" || dsn == "" || redis == "" {
		t.Skip("set dedicated DB/Redis and DEEPAGENT_TEST_CLI/WORKER binaries")
	}
	server, calls := fakeModelServer(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	namespace := protocol.NewID("process")
	wire := fmt.Sprintf("manager:\n  namespace: %s\n  mysql_dsn: %q\n  redis_addr: %q\nworker:\n  concurrency: 2\n  permit_ttl: 3s\n  poll_interval: 20ms\n  shutdown_grace: 1s\ndefault_model: fake\nmodels:\n  - name: fake\n    provider: openai\n    model: fake\n    base_url: %q\n    api_key: test-only\n", namespace, dsn, redis, server.URL+"/v1")
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
	call := func(args ...string) ([]protocol.Event, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		base := []string{"--root", dir, "--config", cfg, "--json"}
		cmd := exec.CommandContext(ctx, cliBin, append(base, args...)...)
		data, err := cmd.Output()
		var events []protocol.Event
		scan := bufio.NewScanner(bytes.NewReader(data))
		for scan.Scan() {
			var e protocol.Event
			if json.Unmarshal(scan.Bytes(), &e) == nil {
				events = append(events, e)
			}
		}
		return events, err
	}
	stop := start()
	events, err := call("--prompt", "write fixture")
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
	stop()
	stop = start()
	events, err = call("--thread", block.ThreadID, "--prompt", "yes")
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
	// Kill the owning process inside a model request. The subscribed CLI must
	// follow the retried input under a new Run after the old permit expires.
	m, err := manager.New(context.Background(), manager.Config{Namespace: namespace, MySQLDSN: dsn, RedisAddr: redis})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	crashThread, err := m.CreateThread(context.Background(), api.CreateThreadRequest{SessionID: protocol.NewID("crash-session"), WorkDir: dir})
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
		e, err := call("--thread", crashThread.ID, "--prompt", "crash fixture")
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
		rows, e := m.ListEvents(context.Background(), api.EventFilter{ThreadID: crashThread.ID, Limit: 100})
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
		t.Fatalf("CLI did not survive owner crash: %v events=%v", recovered.err, recovered.events)
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
