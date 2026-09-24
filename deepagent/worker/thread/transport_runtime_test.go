package thread

import (
	"context"
	"sync"
	"testing"
	"time"

	checkpoints "eino-cli/deepagent/core/checkpoint"
	"eino-cli/deepagent/core/graph"
	"eino-cli/deepagent/core/runtime/agentthread"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	corethread "eino-cli/deepagent/thread"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type bridgeHistory struct {
	mu      sync.Mutex
	records []*agentthread.HistoryRecord
}

func (h *bridgeHistory) Append(_ context.Context, r *agentthread.HistoryRecord) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	r.Seq = int64(len(h.records) + 1)
	if r.MessageID == 0 {
		r.MessageID = r.Seq
	}
	copy := *r
	h.records = append(h.records, &copy)
	return nil
}
func (h *bridgeHistory) List(_ context.Context, q agentthread.ListQuery) ([]*agentthread.HistoryRecord, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []*agentthread.HistoryRecord
	for _, r := range h.records {
		if q.AfterID != nil && r.Seq <= *q.AfterID {
			continue
		}
		copy := *r
		out = append(out, &copy)
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}
func newBridgeRuntime(t *testing.T, ctx context.Context, store compose.CheckPointStore, history *bridgeHistory, approval bool) *Runtime {
	t.Helper()
	bus := make(chan agentthread.Event, 32)
	core := agentthread.New("thread", &agentthread.RunConfig{Agent: graph.Config{Model: &bridgeModel{approval: approval}, DisableSubAgent: true, CheckpointStore: store, ToolDescriptors: []tools.Descriptor{{Tool: bridgeTool{}, RequiresApproval: true}}}}, bus, agentthread.ThreadOptions{HistoryStore: history})
	adapter, err := corethread.NewThread(corethread.AdapterConfig{ThreadID: "thread", SessionID: "session", Thread: core, EventBus: bus})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewTransport(ctx, adapter, api.Thread{ID: "thread", SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}
func awaitBridgeTerminal(t *testing.T, ctx context.Context, r *Runtime) protocol.Event {
	t.Helper()
	for {
		select {
		case event, ok := <-r.Events():
			if !ok {
				t.Fatalf("output closed before terminal: %v", r.Close())
			}
			if event.Terminal() {
				return event
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}
func TestTransportRuntimeResumesApprovalOnNewInstance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := checkpoints.NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	history := &bridgeHistory{}
	first := newBridgeRuntime(t, ctx, store, history, true)
	runID, err := first.PostMessage(ctx, protocol.Input{ID: "input", Kind: protocol.InputUser, Text: "go"})
	if err != nil {
		t.Fatal(err)
	}
	blocked := awaitBridgeTerminal(t, ctx, first)
	if blocked.Kind != protocol.EventBlocked || blocked.RunID != runID || blocked.Block == nil {
		t.Fatalf("blocked=%+v", blocked)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-first.Events(); ok {
		t.Fatal("Close left output open")
	}
	next := newBridgeRuntime(t, ctx, store, history, true)
	defer next.Close()
	id, err := next.PostMessage(ctx, protocol.Input{ID: "answer", Kind: protocol.InputResume, Resume: &protocol.Resume{Kind: "approval", RunID: runID, CheckpointID: blocked.Block.CheckpointID, InterruptID: blocked.Block.InterruptID, Approved: false}})
	if err != nil || id != runID {
		t.Fatalf("resume=%s err=%v", id, err)
	}
	final := awaitBridgeTerminal(t, ctx, next)
	if final.Kind != protocol.EventRunCompleted || final.RunID != runID {
		t.Fatalf("final=%+v", final)
	}
}
func TestTransportRuntimeCloseDrainsCompletedRun(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, err := checkpoints.NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := newBridgeRuntime(t, ctx, store, &bridgeHistory{}, false)
	if _, err = runtime.PostMessage(ctx, protocol.Input{ID: "input", Kind: protocol.InputUser, Text: "go"}); err != nil {
		t.Fatal(err)
	}
	final := awaitBridgeTerminal(t, ctx, runtime)
	if final.Kind != protocol.EventRunCompleted {
		t.Fatal(final)
	}
	if err = runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-runtime.Events(); ok {
		t.Fatal("output channel open")
	}
	if _, err = runtime.PostMessage(ctx, protocol.Input{Kind: protocol.InputUser, Text: "late"}); err == nil {
		t.Fatal("accepted after close")
	}
}

type waitingBridgeModel struct{ started chan struct{} }

func (m *waitingBridgeModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (*waitingBridgeModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	panic("must stream")
}
func (m *waitingBridgeModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	close(m.started)
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestTransportRuntimeParentCancellationPreservesTerminalDelivery(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	defer cancelParent()
	verify, cancelVerify := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelVerify()
	store, err := checkpoints.NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	model := &waitingBridgeModel{started: make(chan struct{})}
	bus := make(chan agentthread.Event, 32)
	core := agentthread.New("thread", &agentthread.RunConfig{Agent: graph.Config{Model: model, DisableSubAgent: true, CheckpointStore: store}}, bus, agentthread.ThreadOptions{})
	source, err := corethread.NewThread(corethread.AdapterConfig{ThreadID: "thread", Thread: core, EventBus: bus})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewTransport(parent, source, api.Thread{ID: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err = runtime.PostMessage(parent, protocol.Input{ID: "input", Kind: protocol.InputUser, Text: "go"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-model.started:
	case <-verify.Done():
		t.Fatal(verify.Err())
	}
	cancelParent()
	final := awaitBridgeTerminal(t, verify, runtime)
	if final.Kind != protocol.EventRunCancelled {
		t.Fatalf("cancel terminal=%+v", final)
	}
}

func TestTransportRuntimeManualCompactHasTerminal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	store, err := checkpoints.NewFile(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runtime := newBridgeRuntime(t, ctx, store, &bridgeHistory{}, false)
	defer runtime.Close()
	id, err := runtime.PostMessage(ctx, protocol.Input{ID: "compact-input", Kind: protocol.InputCompact})
	if err != nil {
		t.Fatal(err)
	}
	final := awaitBridgeTerminal(t, ctx, runtime)
	if final.Kind != protocol.EventRunCompleted || final.RunID != id || len(final.MessageIDs) != 1 || final.MessageIDs[0] != "compact-input" {
		t.Fatalf("compact terminal=%+v", final)
	}
}

type blockingBridgeCompactor struct{ started, canceled, release chan struct{} }

func (*blockingBridgeCompactor) ID() string { return "blocking" }
func (c *blockingBridgeCompactor) Compact(ctx context.Context, _ []*schema.Message) (*agentthread.CompactionResult, error) {
	close(c.started)
	<-ctx.Done()
	close(c.canceled)
	<-c.release
	return nil, ctx.Err()
}
func (*blockingBridgeCompactor) Resume(context.Context, *agentthread.CompactRecord, []*schema.Message) (*agentthread.ResumeResult, error) {
	panic("unused")
}
func TestTransportRuntimeCloseWaitsForManualCompact(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	compactor := &blockingBridgeCompactor{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	bus := make(chan agentthread.Event, 32)
	core := agentthread.New("thread", &agentthread.RunConfig{}, bus, agentthread.ThreadOptions{CompactionStrategy: compactor})
	source, err := corethread.NewThread(corethread.AdapterConfig{ThreadID: "thread", Thread: core, EventBus: bus})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := NewTransport(ctx, source, api.Thread{ID: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	posted := make(chan error, 1)
	go func() {
		_, err := runtime.PostMessage(ctx, protocol.Input{ID: "compact", Kind: protocol.InputCompact})
		posted <- err
	}()
	select {
	case <-compactor.started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	select {
	case <-compactor.canceled:
	case <-ctx.Done():
		t.Fatal("Close did not cancel compaction")
	}
	select {
	case err := <-closed:
		t.Fatalf("closed before compaction cleanup: %v", err)
	default:
	}
	close(compactor.release)
	final := awaitBridgeTerminal(t, ctx, runtime)
	if final.Kind != protocol.EventRunCancelled {
		t.Fatalf("compact terminal=%+v", final)
	}
	select {
	case err := <-posted:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}
