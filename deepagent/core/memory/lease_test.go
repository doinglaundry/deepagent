package memory

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	memorypkg "eino-cli/deepagent/protocol/memory"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type leaseLostStore struct{ *memoryStore }

func (*leaseLostStore) RenewMemory(context.Context, memorypkg.Lease, time.Duration) (memorypkg.Lease, error) {
	return memorypkg.Lease{}, memorypkg.ErrLeaseLost
}

type waitingMemoryModel struct {
	entered chan struct{}
	calls   atomic.Int32
}

func (m *waitingMemoryModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (*waitingMemoryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("bypassed Graph")
}
func (m *waitingMemoryModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.calls.Add(1) == 1 {
		close(m.entered)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestMemory_ScopeLeaseAndDuplicateExtraction(t *testing.T) {
	t.Run("scope and duplicate", func(t *testing.T) {
		ctx := context.Background()
		store := newMemoryStore()
		m := &memoryModel{}
		cfg := Config{Root: t.TempDir(), Model: m, Store: store, Consolidator: func(context.Context, string, string) (string, error) { return "summary", nil }}
		first, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		input := []*schema.Message{schema.UserMessage("stable fact")}
		for range 2 {
			if err := first.Observe(ctx, "user/one", "same-thread", input); err != nil {
				t.Fatal(err)
			}
		}
		if m.calls != 1 {
			t.Fatalf("duplicate extracted %d times", m.calls)
		}
		second, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := second.Observe(ctx, "user/two", "same-thread", input); err != nil {
			t.Fatal(err)
		}
		if m.calls != 2 {
			t.Fatal("separate scope reused another user's source")
		}
		if err := first.Consolidate(ctx, "user/one"); err != nil {
			t.Fatal(err)
		}
		if text, err := second.Read(ctx, "user/two"); err != nil || text.Summary != "" {
			t.Fatalf("scope leaked: %q %v", text, err)
		}
	})
	t.Run("lost lease cancels Graph and fences persistence", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		store := &leaseLostStore{newMemoryStore()}
		m := &waitingMemoryModel{entered: make(chan struct{})}
		p, err := New(Config{Root: t.TempDir(), Model: m, Store: store, LeaseTTL: 90 * time.Millisecond, Consolidator: func(context.Context, string, string) (string, error) { return "", nil }})
		if err != nil {
			t.Fatal(err)
		}
		input := []*schema.Message{schema.UserMessage("fact")}
		done := make(chan error, 1)
		go func() { done <- p.Observe(ctx, "user/one", "thread", input) }()
		select {
		case <-m.entered:
		case <-ctx.Done():
			t.Fatal("extraction never started")
		}
		if err := p.Observe(ctx, "user/one", "thread", input); !errors.Is(err, memorypkg.ErrConflict) {
			t.Fatalf("second owner accepted: %v", err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, memorypkg.ErrLeaseLost) {
				t.Fatalf("lost lease error discarded: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("lost lease did not cancel Graph")
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		if len(store.artifacts) != 0 || len(store.leases) != 0 || m.calls.Load() != 1 {
			t.Fatalf("artifacts=%d leases=%d calls=%d", len(store.artifacts), len(store.leases), m.calls.Load())
		}
	})
}
