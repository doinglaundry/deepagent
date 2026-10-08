package threadhost

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	dalmodel "eino-cli/deepagent/dal/model"
	"eino-cli/deepagent/graph/execution"
	"eino-cli/deepagent/manager"
	messagepkg "eino-cli/deepagent/message"
	"eino-cli/deepagent/run"
	threadpkg "eino-cli/deepagent/thread"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type managerProbe struct {
	mu          sync.Mutex
	renewErr    error
	saveErr     error
	saved       []manager.OutputFrame
	order       []string
	released    bool
	closed      bool
	renews      int
	releaseDone chan struct{}
}

func (*managerProbe) Acquire(context.Context, manager.AcquireRequest) (manager.AcquireResult, error) {
	return manager.AcquireResult{}, nil
}

func (m *managerProbe) Renew(_ context.Context, threadID int64, token string, _ int64) (*manager.Lease, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.renews++
	if m.renewErr != nil {
		return nil, m.renewErr
	}
	return &manager.Lease{ThreadID: threadID, LeaseToken: token, LeaseUntil: time.Now().Add(time.Second)}, nil
}

func (m *managerProbe) ReleaseThread(_ context.Context, _ int64, _ string) (*dalmodel.Thread, error) {
	m.mu.Lock()
	m.released = true
	m.order = append(m.order, "release")
	if m.releaseDone != nil {
		close(m.releaseDone)
	}
	m.mu.Unlock()
	return &dalmodel.Thread{}, nil
}

func (*managerProbe) AckInput(context.Context, int64, string, string, []int64) ([]*dalmodel.Message, error) {
	return nil, nil
}

func (m *managerProbe) ConfirmThreadClosed(context.Context, int64, string, int64) (*manager.ThreadMessageResult, error) {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	return nil, nil
}

func (m *managerProbe) SaveOutput(_ context.Context, _ int64, _ string, _ string, frames []manager.OutputFrame) error {
	m.mu.Lock()
	if m.saveErr != nil {
		err := m.saveErr
		m.mu.Unlock()
		return err
	}
	m.saved = append(m.saved, frames...)
	m.order = append(m.order, "save")
	m.mu.Unlock()
	return nil
}

// Only the model and Manager I/O are substituted; the execution Thread is real.
func newHostThread(t *testing.T, chatModel model.ToolCallingChatModel, closeResources func(context.Context) error) *threadpkg.Thread {
	t.Helper()
	if chatModel == nil {
		chatModel = &runtimeModel{}
	}
	thread, err := threadpkg.NewThread(threadpkg.ThreadConfig{
		ThreadID: "1", CloseResources: closeResources,
		RunConfig: &run.Config{Graph: execution.Config{Model: chatModel}},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = thread.Init(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = thread.Close(ctx)
	})
	return thread
}

type pausedHostModel struct {
	runtimeModel
	started chan struct{}
	release chan struct{}
}

func (m *pausedHostModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *pausedHostModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	close(m.started)
	select {
	case <-m.release:
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("answer", nil)}), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func startPausedRun(t *testing.T, thread *threadpkg.Thread, chatModel *pausedHostModel) *run.Handle {
	t.Helper()
	posted, err := thread.SubmitInput(context.Background(), messagepkg.NewUserMessage("hello"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-chatModel.started:
	case <-time.After(time.Second):
		t.Fatal("model never started")
	}
	return posted.RunHandle
}

func testClaim() *manager.AcquireResult {
	return &manager.AcquireResult{
		Thread: &dalmodel.Thread{ThreadID: 1},
		Lease:  &manager.Lease{ThreadID: 1, LeaseToken: "lease", LeaseUntil: time.Now().Add(time.Second)},
	}
}

// Feed the Host output boundary directly for malformed and precisely ordered
// output cases. Thread initialization, active Run and resource cleanup are real.
func runTestThread(host *ThreadHost, thread *threadpkg.Thread, ctx, acceptCtx context.Context, claim *manager.AcquireResult, items <-chan threadpkg.TransportThreadOutputItem) error {
	host.normalize()
	runCtx, stopLease, waitLease := host.startLease(ctx, claim.Lease)
	defer stopLease()
	run := &threadRun{
		host: host, ctx: runCtx, acceptDone: acceptCtx.Done(), claim: claim, thread: thread,
		idleSince: time.Now(), wasActive: thread.ActiveRun() != nil,
	}
	result, closeErr := run.run(items)
	return run.finish(result, closeErr, waitLease)
}

func testHost(client *managerProbe) *ThreadHost {
	return &ThreadHost{
		Config: Config{MessagePollInterval: time.Millisecond, IdleTimeout: time.Hour},
		Client: client,
	}
}

func TestThreadHost_PersistsEventBeforeYield(t *testing.T) {
	client := &managerProbe{}
	thread := newHostThread(t, nil, nil)
	output := make(chan threadpkg.TransportThreadOutputItem, 2)
	output <- threadpkg.TransportThreadOutputItem{Event: &threadpkg.TransportEvent{RunID: "run-1", Type: "text", Payload: []byte("answer")}}
	output <- threadpkg.TransportThreadOutputItem{Yield: &threadpkg.TransportThreadYield{Reason: "done"}}
	close(output)
	err := runTestThread(testHost(client), thread, context.Background(), context.Background(), testClaim(), output)
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.saved) != 1 || !client.released || len(client.order) != 2 || client.order[0] != "save" || client.order[1] != "release" {
		t.Fatalf("saved=%d released=%v order=%v", len(client.saved), client.released, client.order)
	}
}

func TestThreadHost_FinishedRunKeepsThreadUntilIdleTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client := &managerProbe{releaseDone: make(chan struct{})}
	thread := newHostThread(t, nil, nil)
	output, err := thread.Init(ctx)
	if err != nil {
		t.Fatal(err)
	}
	host := testHost(client)
	host.IdleTimeout = 100 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- runTestThread(host, thread, ctx, ctx, testClaim(), output.Items) }()

	for _, text := range []string{"first", "second"} {
		posted, err := thread.SubmitInput(ctx, messagepkg.NewUserMessage(text))
		if err != nil {
			t.Fatal(err)
		}
		err = posted.RunHandle.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
		// The Host must persist completion and keep this Thread available for
		// another Run instead of immediately destroying its command ledger.
		select {
		case <-client.releaseDone:
			t.Fatal("finished Run released the Thread before its idle timeout")
		case <-time.After(20 * time.Millisecond):
		}
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("idle Thread was never released")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	completions := 0
	for _, frame := range client.saved {
		if frame.EventType == "run_status" {
			var payload struct{ Status string }
			err = json.Unmarshal(frame.Payload, &payload)
			if err != nil {
				t.Fatal(err)
			}
			if payload.Status == "finished" {
				completions++
			}
		}
	}
	if completions != 2 || client.order[len(client.order)-1] != "release" {
		t.Fatalf("completions=%d order=%v", completions, client.order)
	}
}

func TestThreadHost_FailedRunReleasesThreadWithoutWaitingForIdle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	client := &managerProbe{}
	// Missing model causes a real Run to fail during Graph construction.
	thread, err := threadpkg.NewThread(threadpkg.ThreadConfig{ThreadID: "1", RunConfig: &run.Config{}})
	if err != nil {
		t.Fatal(err)
	}
	defer thread.Close(ctx)
	output, err := thread.Init(ctx)
	if err != nil {
		t.Fatal(err)
	}
	host := testHost(client)
	done := make(chan error, 1)
	go func() { done <- runTestThread(host, thread, ctx, ctx, testClaim(), output.Items) }()
	_, err = thread.SubmitInput(ctx, messagepkg.NewUserMessage("fail"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("failed Run incorrectly waited for the Thread idle timeout")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if !client.released || client.order[len(client.order)-1] != "release" {
		t.Fatalf("failed output was not saved before release: %v", client.order)
	}
}

func TestThreadHost_ShutdownWaitsForDelayedFinalOutput(t *testing.T) {
	client := &managerProbe{}
	closing := make(chan struct{})
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	chatModel := &pausedHostModel{started: make(chan struct{}), release: make(chan struct{})}
	thread := newHostThread(t, chatModel, func(context.Context) error {
		close(closing)
		<-gate
		return nil
	})
	handle := startPausedRun(t, thread, chatModel)
	output := make(chan threadpkg.TransportThreadOutputItem)
	host := testHost(client)
	host.ShutdownDrainTimeout = time.Second
	acceptCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runTestThread(host, thread, context.Background(), acceptCtx, testClaim(), output) }()
	close(chatModel.release)
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	err := handle.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-closing:
	case <-ctx.Done():
		t.Fatal("Thread resource cleanup never started")
	}
	output <- threadpkg.TransportThreadOutputItem{
		Event: &threadpkg.TransportEvent{RunID: handle.RunID(), Type: "text", Payload: []byte("final")},
		Yield: &threadpkg.TransportThreadYield{Reason: "finished"},
	}
	release()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Host did not finish")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.saved) != 1 || !client.released {
		t.Fatalf("saved=%d released=%v", len(client.saved), client.released)
	}
}

func TestThreadHost_CloseFailureDoesNotConfirmOrRelease(t *testing.T) {
	client := &managerProbe{}
	failure := errors.New("close failed")
	thread := newHostThread(t, nil, func(context.Context) error { return failure })
	claim := testClaim()
	claim.PendingMessages = []*dalmodel.Message{{MessageID: 7, ThreadID: 1, MessageType: MessageTypeControlCloseThread}}
	err := runTestThread(testHost(client), thread, context.Background(), context.Background(), claim, make(chan threadpkg.TransportThreadOutputItem))
	if !errors.Is(err, failure) {
		t.Fatalf("close error=%v", err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed || client.released {
		t.Fatalf("closed=%v released=%v", client.closed, client.released)
	}
}

func TestLeaseLossPreventsRelease(t *testing.T) {
	client := &managerProbe{renewErr: errors.New("lease lost")}
	closed := make(chan error, 1)
	chatModel := &pausedHostModel{started: make(chan struct{})}
	thread := newHostThread(t, chatModel, func(ctx context.Context) error {
		closed <- ctx.Err()
		return nil
	})
	startPausedRun(t, thread, chatModel)
	host := testHost(client)
	host.LeaseMS = int64(time.Hour / time.Millisecond)
	claim := testClaim()
	claim.Lease.LeaseUntil = time.Now().Add(500 * time.Millisecond)
	err := runTestThread(host, thread, context.Background(), context.Background(), claim, make(chan threadpkg.TransportThreadOutputItem))
	if !errors.Is(err, client.renewErr) {
		t.Fatalf("lease error=%v", err)
	}
	select {
	case closeErr := <-closed:
		if closeErr != nil {
			t.Fatalf("cleanup context canceled: %v", closeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Thread resources not closed")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.released || client.renews == 0 {
		t.Fatalf("released=%v renews=%d", client.released, client.renews)
	}
}

func TestBlockedRunEndIsSavedBeforeRelease(t *testing.T) {
	client := &managerProbe{}
	thread := newHostThread(t, nil, nil)
	output := make(chan threadpkg.TransportThreadOutputItem, 1)
	output <- threadpkg.TransportThreadOutputItem{
		Event: &threadpkg.TransportEvent{RunID: "run-1", Type: "run_status", Payload: []byte(`{"status":"blocked","checkpoint_id":"checkpoint-1","interrupt_id":"interrupt-1"}`)},
		Yield: &threadpkg.TransportThreadYield{Reason: "blocked"},
	}
	close(output)
	err := runTestThread(testHost(client), thread, context.Background(), context.Background(), testClaim(), output)
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if !client.released || len(client.saved) != 1 || len(client.order) != 2 || client.order[0] != "save" || client.order[1] != "release" {
		t.Fatalf("released=%v saved=%v order=%v", client.released, client.saved, client.order)
	}
}

func TestCloseControlConfirmsThreadClosed(t *testing.T) {
	client := &managerProbe{}
	thread := newHostThread(t, nil, nil)
	claim := testClaim()
	claim.PendingMessages = []*dalmodel.Message{{MessageID: 7, ThreadID: 1, MessageType: MessageTypeControlCloseThread, Payload: []byte(`{"reason":"done"}`)}}
	err := runTestThread(testHost(client), thread, context.Background(), context.Background(), claim, make(chan threadpkg.TransportThreadOutputItem))
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if !client.closed || client.released {
		t.Fatalf("closed=%v released=%v", client.closed, client.released)
	}
}

func TestThreadHost_DrainsOutputProducedDuringClose(t *testing.T) {
	for _, failSave := range []bool{false, true} {
		t.Run(map[bool]string{false: "save", true: "save failure"}[failSave], func(t *testing.T) {
			client := &managerProbe{}
			if failSave {
				client.saveErr = errors.New("storage unavailable")
			}
			output := make(chan threadpkg.TransportThreadOutputItem)
			thread := newHostThread(t, nil, func(context.Context) error {
				output <- threadpkg.TransportThreadOutputItem{Event: &threadpkg.TransportEvent{RunID: "run-1", Type: "text", Payload: []byte("final during close")}}
				close(output)
				return nil
			})
			claim := testClaim()
			claim.PendingMessages = []*dalmodel.Message{{MessageID: 7, ThreadID: 1, MessageType: MessageTypeControlCloseThread}}
			host := testHost(client)
			host.ShutdownInterruptDrainTimeout = 500 * time.Millisecond
			err := runTestThread(host, thread, context.Background(), context.Background(), claim, output)
			client.mu.Lock()
			defer client.mu.Unlock()
			if failSave {
				if !errors.Is(err, client.saveErr) || client.closed || client.released {
					t.Fatalf("error=%v closed=%v released=%v", err, client.closed, client.released)
				}
				return
			}
			if err != nil || len(client.saved) != 1 || !client.closed {
				t.Fatalf("error=%v saved=%d closed=%v", err, len(client.saved), client.closed)
			}
		})
	}
}

func TestThreadHost_CloseTimeoutKeepsDrainingUntilThreadStops(t *testing.T) {
	client := &managerProbe{}
	output := make(chan threadpkg.TransportThreadOutputItem)
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	defer release()
	closed := make(chan struct{})
	thread := newHostThread(t, nil, func(context.Context) error {
		<-gate
		output <- threadpkg.TransportThreadOutputItem{Event: &threadpkg.TransportEvent{RunID: "run-1", Type: "text", Payload: []byte("final during close")}}
		close(output)
		close(closed)
		return nil
	})
	claim := testClaim()
	claim.PendingMessages = []*dalmodel.Message{{MessageID: 7, ThreadID: 1, MessageType: MessageTypeControlCloseThread}}
	host := testHost(client)
	host.ShutdownInterruptDrainTimeout = 10 * time.Millisecond
	err := runTestThread(host, thread, context.Background(), context.Background(), claim, output)
	release()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close error=%v", err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("late terminal send blocked after host timeout")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed || client.released {
		t.Fatal("released ownership before Thread stopped")
	}
}

func TestThreadHost_OutputConversionFailurePreventsRelease(t *testing.T) {
	expected := errors.New("event conversion failed")
	output := make(chan threadpkg.TransportThreadOutputItem, 1)
	output <- threadpkg.TransportThreadOutputItem{Err: expected}
	close(output)
	client := &managerProbe{}
	thread := newHostThread(t, nil, nil)
	err := runTestThread(testHost(client), thread, context.Background(), context.Background(), testClaim(), output)
	client.mu.Lock()
	defer client.mu.Unlock()
	if !errors.Is(err, expected) || client.closed || client.released {
		t.Fatalf("error=%v closed=%v released=%v", err, client.closed, client.released)
	}
}

func TestThreadHost_InterruptTimeoutPersistsBeforeRelease(t *testing.T) {
	for _, scenario := range []string{"cancel", "shutdown"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			client := &managerProbe{}
			chatModel := &pausedHostModel{started: make(chan struct{})}
			thread := newHostThread(t, chatModel, nil)
			output, err := thread.Init(ctx)
			if err != nil {
				t.Fatal(err)
			}
			handle := startPausedRun(t, thread, chatModel)
			host := testHost(client)
			host.InterruptDrainTimeout = 200 * time.Millisecond
			host.ShutdownDrainTimeout = 20 * time.Millisecond
			host.ShutdownInterruptDrainTimeout = 200 * time.Millisecond
			claim := testClaim()
			acceptCtx, stopAccept := context.WithCancel(ctx)
			defer stopAccept()
			if scenario == "cancel" {
				claim.PendingMessages = []*dalmodel.Message{{
					ThreadID: 1, MessageID: 7, MessageType: MessageTypeControlCancelInput,
					Payload: []byte(`{"cutoff_message_id":101}`),
				}}
			} else {
				stopAccept()
			}
			err = runTestThread(host, thread, ctx, acceptCtx, claim, output.Items)
			if err != nil {
				t.Fatal(err)
			}
			err = handle.Wait(ctx)
			if err != nil {
				t.Fatal(err)
			}
			client.mu.Lock()
			defer client.mu.Unlock()
			interrupted := false
			for _, frame := range client.saved {
				if frame.RunID != handle.RunID() || frame.EventType != "run_status" {
					continue
				}
				var payload struct {
					Status string `json:"status"`
				}
				err = json.Unmarshal(frame.Payload, &payload)
				if err != nil {
					t.Fatal(err)
				}
				interrupted = interrupted || payload.Status == "interrupted"
			}
			if !interrupted || !client.released || len(client.order) == 0 || client.order[len(client.order)-1] != "release" {
				t.Fatalf("terminal interrupt must be saved before release: interrupted=%v released=%v order=%v", interrupted, client.released, client.order)
			}
		})
	}
}
