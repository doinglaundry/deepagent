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
	mu       sync.Mutex
	renewErr error
	saved    []manager.OutputFrame
	released bool
	status   dalmodel.ThreadStatus
	closed   bool
	renews   int
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
	m.status = status
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
	m.saved = append(m.saved, frames...)
	m.mu.Unlock()
	return nil
}

type runtimeProbe struct {
	output chan threadpkg.TransportThreadOutputItem
	active *threadpkg.TransportActiveRun
	closed chan struct{}
}

func (r *runtimeProbe) Init(context.Context) (*threadpkg.TransportThreadOutput, error) {
	return &threadpkg.TransportThreadOutput{Items: r.output}, nil
}

func (*runtimeProbe) PostMessage(context.Context, *threadpkg.TransportMessage) (*threadpkg.TransportPostMessageResult, error) {
	return nil, nil
}

func (*runtimeProbe) Interrupt(context.Context, threadpkg.TransportThreadInterruptRequest) error {
	return nil
}
func (r *runtimeProbe) ActiveRun() *threadpkg.TransportActiveRun { return r.active }
func (r *runtimeProbe) Close(context.Context) error {
	if r.closed != nil {
		close(r.closed)
	}
	return nil
}

func testClaim() *manager.AcquireResult {
	return &manager.AcquireResult{
		Thread: &dalmodel.Thread{ThreadID: 1},
		Lease:  &manager.Lease{ThreadID: 1, LeaseToken: "lease", LeaseUntil: time.Now().Add(time.Second)},
	}
}

func TestRunThreadPersistsOutputBeforeRelease(t *testing.T) {
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
	if len(client.saved) != 1 || !client.released {
		t.Fatalf("saved=%d released=%v", len(client.saved), client.released)
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
