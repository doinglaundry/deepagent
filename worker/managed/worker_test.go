package managed

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"eino-cli/manager/api"
	"eino-cli/protocol"
)

type boundaryManager struct {
	api.Manager
	mu       sync.Mutex
	claim    api.Claim
	claimed  bool
	ack      []string
	events   []protocol.Event
	released *api.Release
	closed   bool
	renewErr error
	renews   int
	done     chan struct{}
}

func (m *boundaryManager) ScanRunnableThreads(context.Context, int) ([]api.Thread, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimed {
		return nil, nil
	}
	return []api.Thread{m.claim.Thread}, nil
}
func (m *boundaryManager) ClaimThread(context.Context, string, string, time.Duration) (api.Claim, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.claimed {
		return api.Claim{}, api.ErrConflict
	}
	m.claimed = true
	return m.claim, nil
}
func (m *boundaryManager) RenewThreadPermit(_ context.Context, p api.Permit, _ time.Duration) (api.Permit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.renews++
	return p, m.renewErr
}
func (m *boundaryManager) ReadPendingInputs(context.Context, api.Permit) ([]protocol.Input, error) {
	return nil, nil
}
func (m *boundaryManager) ConfirmInputDelivery(_ context.Context, _ api.Permit, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ack = append(m.ack, id)
	return nil
}
func (m *boundaryManager) PublishEvent(_ context.Context, _ api.Permit, e protocol.Event) (protocol.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return e, nil
}
func (m *boundaryManager) ReleaseThread(_ context.Context, _ api.Permit, r api.Release) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.released = &r
	close(m.done)
	return nil
}
func (m *boundaryManager) ConfirmThreadClosed(context.Context, api.Permit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	close(m.done)
	return nil
}

type boundaryRuntime struct {
	events chan protocol.Event
	post   func(protocol.Input)
	cancel func()
	once   sync.Once
}

func (r *boundaryRuntime) PostMessage(_ context.Context, i protocol.Input) (string, error) {
	if r.post != nil {
		r.post(i)
	}
	return "run", nil
}
func (r *boundaryRuntime) Cancel() {
	if r.cancel != nil {
		r.cancel()
	}
}
func (r *boundaryRuntime) Events() <-chan protocol.Event { return r.events }
func (r *boundaryRuntime) Close() error                  { r.once.Do(func() { close(r.events) }); return nil }
func testManager(in ...protocol.Input) *boundaryManager {
	return &boundaryManager{claim: api.Claim{Thread: api.Thread{ID: "thread", Namespace: "ns", SessionID: "session", State: api.Running}, Permit: api.Permit{ThreadID: "thread", Token: "token"}, Inputs: in}, done: make(chan struct{})}
}
func runWorker(t *testing.T, m *boundaryManager, r *boundaryRuntime) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	w, err := New(m, func(context.Context, api.Claim) (Runtime, error) { return r, nil }, Config{PollInterval: time.Millisecond, PermitTTL: 90 * time.Millisecond, ShutdownGrace: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("worker shutdown hung")
		}
	})
	return cancel, done
}
func await(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(time.Second):
		t.Fatal("worker did not finish")
	}
}
func TestOutputDrainsBeforeReleaseAndAcknowledgesAcceptance(t *testing.T) {
	m := testManager(protocol.Input{ID: "input", Kind: protocol.InputUser, Text: "hello"})
	r := &boundaryRuntime{events: make(chan protocol.Event, 8)}
	r.post = func(protocol.Input) {
		r.events <- protocol.Event{Kind: protocol.EventText, Text: "answer"}
		r.events <- protocol.Event{Kind: protocol.EventRunCompleted, RunID: "run"}
		r.events <- protocol.Event{Kind: protocol.EventTokens}
	}
	runWorker(t, m, r)
	await(t, m.done)
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.ack) != 1 || len(m.events) != 3 || m.released == nil {
		t.Fatalf("ack=%v events=%v release=%v", m.ack, m.events, m.released)
	}
}
func TestBlockedEventReleasesBlock(t *testing.T) {
	m := testManager(protocol.Input{ID: "input", Kind: protocol.InputUser, Text: "hello"})
	r := &boundaryRuntime{events: make(chan protocol.Event, 4)}
	r.post = func(protocol.Input) {
		r.events <- protocol.Event{Kind: protocol.EventBlocked, RunID: "run", Block: &protocol.Block{RunID: "run", CheckpointID: "checkpoint", InterruptID: "gate"}}
	}
	runWorker(t, m, r)
	await(t, m.done)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.released == nil || m.released.Block == nil || m.released.Block.CheckpointID != "checkpoint" {
		t.Fatalf("bad release: %+v", m.released)
	}
}
func TestCloseControlCancelsAndConfirmsClosed(t *testing.T) {
	m := testManager(protocol.Input{ID: "close", Kind: protocol.InputClose})
	cancelled := make(chan struct{})
	r := &boundaryRuntime{events: make(chan protocol.Event, 4), cancel: func() { close(cancelled) }}
	runWorker(t, m, r)
	await(t, m.done)
	await(t, cancelled)
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.closed {
		t.Fatal("not confirmed closed")
	}
}
func TestRenewalLossStopsEngineWithoutRelease(t *testing.T) {
	m := testManager(protocol.Input{ID: "input", Kind: protocol.InputUser, Text: "hello"})
	m.renewErr = api.ErrPermitLost
	cancelled := make(chan struct{})
	var once sync.Once
	r := &boundaryRuntime{events: make(chan protocol.Event, 4), cancel: func() { once.Do(func() { close(cancelled) }) }}
	runWorker(t, m, r)
	await(t, cancelled)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.released != nil || m.closed {
		t.Fatal("lost owner mutated state")
	}
	if m.renews == 0 {
		t.Fatal("lease never renewed")
	}
}
func TestRejectInvalidConfig(t *testing.T) {
	_, err := New(nil, nil, Config{})
	if err == nil {
		t.Fatal("expected validation error")
	}
	_ = errors.Is(err, api.ErrConflict)
}
