package memory

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type leaseLostStore struct{ *memoryStore }

func (*leaseLostStore) RenewMemory(context.Context, agentmodel.MemoryLease, time.Duration) (agentmodel.MemoryLease, error) {
	return agentmodel.MemoryLease{}, agentmodel.ErrMemoryLeaseLost
}

type waitingMemoryModel struct {
	entered chan struct{}
	calls   atomic.Int32
}

func (chatModel *waitingMemoryModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return chatModel, nil
}
func (*waitingMemoryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("bypassed Graph")
}
func (chatModel *waitingMemoryModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if chatModel.calls.Add(1) == 1 {
		close(chatModel.entered)
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestMemory_ScopeLeaseAndDuplicateExtraction(t *testing.T) {
	t.Run("scope and duplicate", func(t *testing.T) {
		ctx := context.Background()
		store := newMemoryStore()
		chatModel := &memoryModel{}
		config := Config{Root: t.TempDir(), Model: chatModel, Store: store, Consolidator: func(context.Context, string, string) (string, error) { return "summary", nil }}
		first, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		input := []*agentmodel.Message{agentmodel.NewUserMessage("stable fact")}
		for range 2 {
			err := first.Observe(ctx, "user/one", "same-thread", input)
			if err != nil {
				t.Fatal(err)
			}
		}
		if chatModel.calls != 1 {
			t.Fatalf("duplicate extracted %d times", chatModel.calls)
		}
		second, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		observeErr := second.Observe(ctx, "user/two", "same-thread", input)
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		if chatModel.calls != 2 {
			t.Fatal("separate scope reused another user's source")
		}
		consolidateErr := first.Consolidate(ctx, "user/one")
		if consolidateErr != nil {
			t.Fatal(consolidateErr)
		}
		text, readErr := second.Read(ctx, "user/two")
		if readErr != nil || text.Summary != "" {
			t.Fatalf("scope leaked: %q %v", text, readErr)
		}
	})
	t.Run("lost lease cancels Graph and fences persistence", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		store := &leaseLostStore{newMemoryStore()}
		chatModel := &waitingMemoryModel{entered: make(chan struct{})}
		memoryService, err := New(Config{Root: t.TempDir(), Model: chatModel, Store: store, LeaseTTL: 90 * time.Millisecond, Consolidator: func(context.Context, string, string) (string, error) { return "", nil }})
		if err != nil {
			t.Fatal(err)
		}
		input := []*agentmodel.Message{agentmodel.NewUserMessage("fact")}
		done := make(chan error, 1)
		go func() { done <- memoryService.Observe(ctx, "user/one", "thread", input) }()
		select {
		case <-chatModel.entered:
		case <-ctx.Done():
			t.Fatal("extraction never started")
		}
		observeErr := memoryService.Observe(ctx, "user/one", "thread", input)
		if !errors.Is(observeErr, agentmodel.ErrMemoryConflict) {
			t.Fatalf("second owner accepted: %v", observeErr)
		}
		select {
		case err := <-done:
			if !errors.Is(err, agentmodel.ErrMemoryLeaseLost) {
				t.Fatalf("lost lease error discarded: %v", err)
			}
		case <-ctx.Done():
			t.Fatal("lost lease did not cancel Graph")
		}
		store.mu.Lock()
		defer store.mu.Unlock()
		if len(store.artifacts) != 0 || len(store.leases) != 0 || chatModel.calls.Load() != 1 {
			t.Fatalf("artifacts=%d leases=%d calls=%d", len(store.artifacts), len(store.leases), chatModel.calls.Load())
		}
	})
}
