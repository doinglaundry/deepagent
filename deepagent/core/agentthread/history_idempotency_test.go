package agentthread

import (
	"context"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
)

type dedupHistoryStore struct {
	mu      sync.Mutex
	records []*HistoryRecord
	seen    map[int64]struct{}
}

type blockingCompaction struct {
	entered chan struct{}
	release chan struct{}
}

func (blockingCompaction) ID() string { return "blocking" }

func (s blockingCompaction) Compact(context.Context, []*Message) (*CompactionResult, error) {
	close(s.entered)
	<-s.release
	return &CompactionResult{
		Compact: &CompactRecord{Summary: schema.SystemMessage("stale summary"), CompactStrategyID: s.ID()},
		Rebuilt: []*Message{schema.SystemMessage("stale summary")},
	}, nil
}

func (blockingCompaction) Resume(context.Context, *CompactRecord, []*Message) (*ResumeResult, error) {
	return nil, nil
}

func (s *dedupHistoryStore) Append(_ context.Context, record *HistoryRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = make(map[int64]struct{})
	}
	if _, exists := s.seen[record.MessageID]; exists {
		return nil
	}
	s.seen[record.MessageID] = struct{}{}
	copy := *record
	copy.Seq = int64(len(s.records) + 1)
	s.records = append(s.records, &copy)
	return nil
}

func (s *dedupHistoryStore) List(_ context.Context, query ListQuery) ([]*HistoryRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := append([]*HistoryRecord(nil), s.records...)
	if query.Order == ListOrderDESC {
		for left, right := 0, len(result)-1; left < right; left, right = left+1, right-1 {
			result[left], result[right] = result[right], result[left]
		}
	}
	return result, nil
}

func TestHistoryRedeliveryUsesDurableMessageIdentity(t *testing.T) {
	store := &dedupHistoryStore{}
	provider := func(context.Context, string, string, *schema.Message) int64 { return 42 }
	first := NewMemoryContextManager("thread-1", store, nil, nil, WithHistoryRecordIDProvider(provider))
	if err := first.AddHistory(context.Background(), "run-1", schema.UserMessage("once")); err != nil {
		t.Fatal(err)
	}

	second := NewMemoryContextManager("thread-1", store, nil, nil, WithHistoryRecordIDProvider(provider))
	if err := second.ReloadHistory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := second.AddHistory(context.Background(), "run-2", schema.UserMessage("once")); err != nil {
		t.Fatal(err)
	}
	if history := second.History(context.Background()); len(history) != 1 || history[0].Content != "once" {
		t.Fatalf("redelivery duplicated model history: %+v", history)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.records) != 1 {
		t.Fatalf("redelivery duplicated durable history: %d records", len(store.records))
	}
}

func TestStaleCompactionCannotOverwriteAcceptedHistory(t *testing.T) {
	strategy := blockingCompaction{entered: make(chan struct{}), release: make(chan struct{})}
	manager := NewMemoryContextManager("thread-1", nil, strategy, nil)
	if err := manager.AddHistory(context.Background(), "run-1", schema.UserMessage("old")); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := manager.Compact(context.Background(), "compact-1")
		result <- err
	}()
	<-strategy.entered
	if err := manager.AddHistory(context.Background(), "run-2", schema.UserMessage("fresh")); err != nil {
		t.Fatal(err)
	}
	close(strategy.release)
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	history := manager.History(context.Background())
	if len(history) != 2 || history[0].Content != "old" || history[1].Content != "fresh" {
		t.Fatalf("stale compaction replaced accepted history: %+v", history)
	}
}
