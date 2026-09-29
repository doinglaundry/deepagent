package threadhost

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	dalmodel "eino-cli/deepagent/dal/model"
	"eino-cli/deepagent/manager"
	threadpkg "eino-cli/deepagent/thread"
)

type managerProbe struct {
	mu          sync.Mutex
	renewErr    error
	saveErr     error
	saved       []manager.OutputFrame
	order       []string
	released    bool
	status      dalmodel.ThreadStatus
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

func (m *managerProbe) ReleaseThread(_ context.Context, _ int64, _ string, _ string, status dalmodel.ThreadStatus) (*dalmodel.Thread, error) {
	m.mu.Lock()
	m.released = true
	m.order = append(m.order, "release")
	m.status = status
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

type runtimeProbe struct {
	mu                sync.Mutex
	output            chan threadpkg.TransportThreadOutputItem
	active            *threadpkg.TransportActiveRun
	closed            chan struct{}
	initialized       chan struct{}
	activeNilObserved chan struct{}
	closeStarted      chan struct{}
	closeGate         chan struct{}
	closeErr          error
	closeOutput       bool
	closeContextErr   chan error
}

func (r *runtimeProbe) Init(context.Context) (*threadpkg.TransportThreadOutput, error) {
	if r.initialized != nil {
		close(r.initialized)
	}
	return &threadpkg.TransportThreadOutput{Items: r.output}, nil
}

func (*runtimeProbe) PostMessage(context.Context, *threadpkg.TransportMessage) (*threadpkg.TransportPostMessageResult, error) {
	return nil, nil
}

func (*runtimeProbe) Interrupt(context.Context, threadpkg.TransportThreadInterruptRequest) error {
	return nil
}
func (r *runtimeProbe) ActiveRun() *threadpkg.TransportActiveRun {
	r.mu.Lock()
	active := r.active
	activeNilObserved := r.activeNilObserved
	r.mu.Unlock()
	if active == nil && activeNilObserved != nil {
		select {
		case activeNilObserved <- struct{}{}:
		default:
		}
	}
	return active
}
func (r *runtimeProbe) setActive(active *threadpkg.TransportActiveRun) {
	r.mu.Lock()
	r.active = active
	r.mu.Unlock()
}
func (r *runtimeProbe) Close(ctx context.Context) error {
	if r.closeStarted != nil {
		close(r.closeStarted)
	}
	if r.closeGate != nil {
		<-r.closeGate
	}
	if r.closeOutput {
		select {
		case r.output <- threadpkg.TransportThreadOutputItem{Event: &threadpkg.TransportEvent{RunID: "run-1", Type: "text", Payload: []byte("final during close")}}:
		case <-ctx.Done():
			return ctx.Err()
		}
		close(r.output)
	}
	if r.closeContextErr != nil {
		r.closeContextErr <- ctx.Err()
	}
	if r.closed != nil {
		close(r.closed)
	}
	return r.closeErr
}

func testClaim() *manager.AcquireResult {
	return &manager.AcquireResult{
		Thread: &dalmodel.Thread{ThreadID: 1},
		Lease:  &manager.Lease{ThreadID: 1, LeaseToken: "lease", LeaseUntil: time.Now().Add(time.Second)},
	}
}

func TestThreadHost_PersistsEventBeforeYield(t *testing.T) {
	client := &managerProbe{}
	runtime := &runtimeProbe{output: make(chan threadpkg.TransportThreadOutputItem, 2)}
	runtime.output <- threadpkg.TransportThreadOutputItem{Event: &threadpkg.TransportEvent{
		RunID: "run-1", Type: "text", Payload: []byte("answer"),
	}}
	runtime.output <- threadpkg.TransportThreadOutputItem{Yield: &threadpkg.TransportThreadYield{Reason: "done"}}
	close(runtime.output)
	host := &ThreadHost{
		Config: Config{RenewInterval: time.Hour, MessagePollInterval: time.Millisecond, IdleTimeout: time.Hour},
		Client: client,
		ThreadFactory: func(context.Context, *dalmodel.Thread) (threadpkg.ThreadRuntime, error) {
			return runtime, nil
		},
	}
	if err := host.RunThread(context.Background(), context.Background(), testClaim()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.saved) != 1 || !client.released || len(client.order) != 2 || client.order[0] != "save" || client.order[1] != "release" {
		t.Fatalf("saved=%d released=%v order=%v", len(client.saved), client.released, client.order)
	}
}

func TestThreadHost_ShutdownWaitsForDelayedFinalOutput(t *testing.T) {
	client := &managerProbe{releaseDone: make(chan struct{})}
	runtime := &runtimeProbe{
		output:            make(chan threadpkg.TransportThreadOutputItem, 1),
		active:            &threadpkg.TransportActiveRun{RunID: "run-1"},
		initialized:       make(chan struct{}),
		activeNilObserved: make(chan struct{}, 1),
		closeStarted:      make(chan struct{}),
		closeGate:         make(chan struct{}),
	}
	host := &ThreadHost{
		Config: Config{
			RenewInterval:        time.Hour,
			MessagePollInterval:  time.Millisecond,
			IdleTimeout:          time.Hour,
			ShutdownDrainTimeout: time.Second,
		},
		Client: client,
		ThreadFactory: func(context.Context, *dalmodel.Thread) (threadpkg.ThreadRuntime, error) {
			return runtime, nil
		},
	}
	acceptCtx, cancelAccept := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- host.RunThread(context.Background(), acceptCtx, testClaim())
	}()
	select {
	case <-runtime.initialized:
	case <-time.After(time.Second):
		t.Fatal("runtime was not initialized")
	}
	runtime.setActive(nil)
	cancelAccept()
	select {
	case <-runtime.activeNilObserved:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not inspect the inactive run")
	}
	select {
	case <-runtime.closeStarted:
	case <-time.After(100 * time.Millisecond):
	}
	runtime.output <- threadpkg.TransportThreadOutputItem{
		Event: &threadpkg.TransportEvent{RunID: "run-1", Type: "text", Payload: []byte("final")},
		Yield: &threadpkg.TransportThreadYield{Reason: "finished"},
	}
	select {
	case <-runtime.closeStarted:
	case <-time.After(time.Second):
		t.Fatal("runtime close did not start after final output")
	}
	close(runtime.closeGate)
	err := <-done
	if err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if len(client.saved) != 1 || !client.released {
		t.Fatalf("saved=%d released=%v", len(client.saved), client.released)
	}
}

func TestThreadHost_CloseFailureDoesNotConfirmOrRelease(t *testing.T) {
	client := &managerProbe{}
	runtime := &runtimeProbe{
		output:   make(chan threadpkg.TransportThreadOutputItem),
		closeErr: errors.New("close failed"),
	}
	claim := testClaim()
	claim.PendingMessages = []*dalmodel.Message{{
		MessageID: 7, ThreadID: 1, MessageType: MessageTypeControlCloseThread,
	}}
	host := &ThreadHost{
		Config: Config{RenewInterval: time.Hour, MessagePollInterval: time.Millisecond, IdleTimeout: time.Hour},
		Client: client,
		ThreadFactory: func(context.Context, *dalmodel.Thread) (threadpkg.ThreadRuntime, error) {
			return runtime, nil
		},
	}
	err := host.RunThread(context.Background(), context.Background(), claim)
	if err == nil || !errors.Is(err, runtime.closeErr) {
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
	runtime := &runtimeProbe{
		output: make(chan threadpkg.TransportThreadOutputItem),
		active: &threadpkg.TransportActiveRun{RunID: "run-1"},
		closed: make(chan struct{}),
	}
	host := &ThreadHost{
		Config: Config{LeaseMS: 30, RenewInterval: time.Millisecond, MessagePollInterval: time.Millisecond, IdleTimeout: time.Hour},
		Client: client,
		ThreadFactory: func(context.Context, *dalmodel.Thread) (threadpkg.ThreadRuntime, error) {
			return runtime, nil
		},
	}
	err := host.RunThread(context.Background(), context.Background(), testClaim())
	if err == nil {
		t.Fatal("expected lease loss")
	}
	select {
	case <-runtime.closed:
	case <-time.After(time.Second):
		t.Fatal("runtime was not closed")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.released || client.renews == 0 {
		t.Fatalf("released=%v renews=%d", client.released, client.renews)
	}
}

func TestLeaseLossUsesIndependentRuntimeCloseContext(t *testing.T) {
	client := &managerProbe{renewErr: errors.New("lease lost")}
	closeContextErr := make(chan error, 1)
	runtime := &runtimeProbe{
		output:          make(chan threadpkg.TransportThreadOutputItem),
		active:          &threadpkg.TransportActiveRun{RunID: "run-1"},
		closed:          make(chan struct{}),
		closeContextErr: closeContextErr,
	}
	host := &ThreadHost{
		Config: Config{LeaseMS: 30, RenewInterval: time.Millisecond, MessagePollInterval: time.Millisecond, IdleTimeout: time.Hour},
		Client: client,
		ThreadFactory: func(context.Context, *dalmodel.Thread) (threadpkg.ThreadRuntime, error) {
			return runtime, nil
		},
	}
	err := host.RunThread(context.Background(), context.Background(), testClaim())
	if err == nil {
		t.Fatal("expected lease loss")
	}
	select {
	case closeErr := <-closeContextErr:
		if closeErr != nil {
			t.Fatalf("runtime close context was canceled: %v", closeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime close was not called")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.released {
		t.Fatal("released lease after lease loss")
	}
}

func TestBlockedYieldReleasesThreadAsBlocked(t *testing.T) {
	client := &managerProbe{}
	runtime := &runtimeProbe{output: make(chan threadpkg.TransportThreadOutputItem, 1)}
	runtime.output <- threadpkg.TransportThreadOutputItem{Yield: &threadpkg.TransportThreadYield{
		Reason: "waiting for approval",
		Block:  &threadpkg.TransportPendingBlock{RunID: "run-1", CheckpointID: "checkpoint-1", InterruptID: "interrupt-1"},
	}}
	close(runtime.output)
	host := &ThreadHost{
		Config: Config{RenewInterval: time.Hour, MessagePollInterval: time.Millisecond, IdleTimeout: time.Hour},
		Client: client,
		ThreadFactory: func(context.Context, *dalmodel.Thread) (threadpkg.ThreadRuntime, error) {
			return runtime, nil
		},
	}
	if err := host.RunThread(context.Background(), context.Background(), testClaim()); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if !client.released || client.status != dalmodel.ThreadStatusBlocked {
		t.Fatalf("released=%v status=%q", client.released, client.status)
	}
}

func TestCloseControlConfirmsThreadClosed(t *testing.T) {
	client := &managerProbe{}
	runtime := &runtimeProbe{output: make(chan threadpkg.TransportThreadOutputItem)}
	claim := testClaim()
	claim.PendingMessages = []*dalmodel.Message{{
		MessageID: 7, ThreadID: 1, MessageType: MessageTypeControlCloseThread, Payload: []byte(`{"reason":"done"}`),
	}}
	host := &ThreadHost{
		Config: Config{RenewInterval: time.Hour, MessagePollInterval: time.Millisecond, IdleTimeout: time.Hour},
		Client: client,
		ThreadFactory: func(context.Context, *dalmodel.Thread) (threadpkg.ThreadRuntime, error) {
			return runtime, nil
		},
	}
	if err := host.RunThread(context.Background(), context.Background(), claim); err != nil {
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
			runtime := &runtimeProbe{output: make(chan threadpkg.TransportThreadOutputItem), closeOutput: true}
			claim := testClaim()
			claim.PendingMessages = []*dalmodel.Message{{MessageID: 7, ThreadID: 1, MessageType: MessageTypeControlCloseThread}}
			host := &ThreadHost{
				Config:        Config{RenewInterval: time.Hour, MessagePollInterval: time.Millisecond, IdleTimeout: time.Hour, ShutdownInterruptDrainTimeout: 500 * time.Millisecond},
				Client:        client,
				ThreadFactory: func(context.Context, *dalmodel.Thread) (threadpkg.ThreadRuntime, error) { return runtime, nil },
			}
			err := host.RunThread(context.Background(), context.Background(), claim)
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

func TestThreadHost_CloseTimeoutKeepsDrainingUntilRuntimeStops(t *testing.T) {
	client := &managerProbe{}
	runtime := &runtimeProbe{output: make(chan threadpkg.TransportThreadOutputItem), closeOutput: true, closeGate: make(chan struct{}), closed: make(chan struct{})}
	claim := testClaim()
	claim.PendingMessages = []*dalmodel.Message{{MessageID: 7, ThreadID: 1, MessageType: MessageTypeControlCloseThread}}
	host := &ThreadHost{
		Config:        Config{RenewInterval: time.Hour, MessagePollInterval: time.Millisecond, IdleTimeout: time.Hour, ShutdownInterruptDrainTimeout: 10 * time.Millisecond},
		Client:        client,
		ThreadFactory: func(context.Context, *dalmodel.Thread) (threadpkg.ThreadRuntime, error) { return runtime, nil },
	}
	err := host.RunThread(context.Background(), context.Background(), claim)
	close(runtime.closeGate)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("close error=%v", err)
	}
	select {
	case <-runtime.closed:
	case <-time.After(time.Second):
		t.Fatal("late terminal send blocked after host timeout")
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closed || client.released {
		t.Fatal("released ownership before runtime stopped")
	}
}

func TestThreadHost_OutputConversionFailurePreventsRelease(t *testing.T) {
	expected := errors.New("event conversion failed")
	runtime := &runtimeProbe{output: make(chan threadpkg.TransportThreadOutputItem, 1)}
	runtime.output <- threadpkg.TransportThreadOutputItem{Err: expected}
	close(runtime.output)
	client := &managerProbe{}
	host := &ThreadHost{
		Config:        Config{RenewInterval: time.Hour, MessagePollInterval: time.Millisecond, IdleTimeout: time.Hour},
		Client:        client,
		ThreadFactory: func(context.Context, *dalmodel.Thread) (threadpkg.ThreadRuntime, error) { return runtime, nil },
	}
	err := host.RunThread(context.Background(), context.Background(), testClaim())
	if !errors.Is(err, expected) || client.closed || client.released {
		t.Fatalf("error=%v closed=%v released=%v", err, client.closed, client.released)
	}
}
