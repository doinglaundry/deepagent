package conversation

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
)

type testStore struct {
	records []*HistoryRecord
	fail    bool
}

func (s *testStore) Append(_ context.Context, r *HistoryRecord) error {
	if s.fail {
		return errors.New("store failed")
	}
	r.Seq = int64(len(s.records) + 1)
	s.records = append(s.records, r)
	return nil
}
func (s *testStore) List(_ context.Context, q ListQuery) ([]*HistoryRecord, error) {
	var out []*HistoryRecord
	for _, r := range s.records {
		if q.AfterID != nil && r.Seq <= *q.AfterID {
			continue
		}
		out = append(out, r)
		if q.Limit > 0 && len(out) >= q.Limit {
			break
		}
	}
	return out, nil
}
func TestContext_PersistFailureDoesNotChangeHistory(t *testing.T) {
	ctx := context.Background()
	store := &testStore{fail: true}
	c := New("thread", store, nil, nil)
	err := c.AddHistory(ctx, "run", schema.UserMessage("hello"))
	if err == nil {
		t.Fatal("expected persistence failure")
	}
	if len(c.History(ctx)) != 0 {
		t.Fatal("failed write appeared in history")
	}
}

type testCompactor struct {
	started chan struct{}
	release chan struct{}
}

func (*testCompactor) ID() string { return "test" }
func (c *testCompactor) Compact(ctx context.Context, m []*Message) (*CompactionResult, error) {
	if c.started != nil {
		close(c.started)
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s := schema.SystemMessage("summary")
	return &CompactionResult{Summary: s, Rebuilt: append([]*Message{s}, m[len(m)-1:]...)}, nil
}
func TestContext_ReloadEqualsCompactedContext(t *testing.T) {
	ctx := context.Background()
	store := &testStore{}
	c := New("thread", store, &testCompactor{}, nil)
	err := c.AddHistory(ctx, "run", schema.UserMessage("older"), schema.UserMessage("retain"))
	if err != nil {
		t.Fatal(err)
	}
	_, compactErr := c.Compact(ctx, "run")
	if compactErr != nil {
		t.Fatal(compactErr)
	}
	addHistoryErr := c.AddHistory(ctx, "run", schema.UserMessage("later"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	restored := New("thread", store, &testCompactor{}, nil)
	reloadHistoryErr := restored.ReloadHistory(ctx)
	if reloadHistoryErr != nil {
		t.Fatal(reloadHistoryErr)
	}
	messages := restored.History(ctx)
	if len(messages) != 3 || messages[0].Content != "summary" || messages[1].Content != "retain" || messages[2].Content != "later" {
		t.Fatalf("retained context lost: %v", messages)
	}
}
func TestContext_StaleCompactionCannotOverwriteNewInput(t *testing.T) {
	ctx := context.Background()
	compactor := &testCompactor{started: make(chan struct{}), release: make(chan struct{})}
	store := &testStore{}
	c := New("thread", store, compactor, nil)
	cAddHistoryErr := c.AddHistory(ctx, "run", schema.UserMessage("before"))
	if cAddHistoryErr != nil {
		t.Fatal(cAddHistoryErr)
	}
	done := make(chan error, 1)
	go func() { _, err := c.Compact(ctx, "run"); done <- err }()
	<-compactor.started
	addHistoryErr := c.AddHistory(ctx, "run", schema.UserMessage("new input"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	close(compactor.release)
	checkErr := <-done
	if checkErr != nil {
		t.Fatal(checkErr)
	}
	h := c.History(ctx)
	if len(h) != 2 || h[0].Content != "before" || h[1].Content != "new input" {
		t.Fatalf("stale summary overwrote input: %v", h)
	}
	if len(store.records) != 2 {
		t.Fatal("stale compact was persisted")
	}
}

type DedupHistoryStore struct {
	mu      sync.Mutex
	records []*HistoryRecord
	seen    map[int64]struct{}
}

type BlockingCompaction struct {
	entered chan struct{}
	release chan struct{}
}

func (BlockingCompaction) ID() string { return "blocking" }

func (s BlockingCompaction) Compact(context.Context, []*Message) (*CompactionResult, error) {
	close(s.entered)
	<-s.release
	summary := schema.SystemMessage("stale summary")
	return &CompactionResult{
		Summary: summary,
		Rebuilt: []*Message{summary},
	}, nil
}

func (s *DedupHistoryStore) Append(_ context.Context, record *HistoryRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seen == nil {
		s.seen = make(map[int64]struct{})
	}
	_, exists := s.seen[record.MessageID]
	if exists {
		return nil
	}
	s.seen[record.MessageID] = struct{}{}
	copy := *record
	copy.Seq = int64(len(s.records) + 1)
	s.records = append(s.records, &copy)
	return nil
}

func (s *DedupHistoryStore) List(_ context.Context, query ListQuery) ([]*HistoryRecord, error) {
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
	store := &DedupHistoryStore{}
	provider := func(context.Context, string, string, *schema.Message) int64 { return 42 }
	first := New("thread-1", store, nil, nil, WithRecordID(provider))
	err := first.AddHistory(context.Background(), "run-1", schema.UserMessage("once"))
	if err != nil {
		t.Fatal(err)
	}

	second := New("thread-1", store, nil, nil, WithRecordID(provider))
	reloadHistoryErr := second.ReloadHistory(context.Background())
	if reloadHistoryErr != nil {
		t.Fatal(reloadHistoryErr)
	}
	addHistoryErr := second.AddHistory(context.Background(), "run-2", schema.UserMessage("once"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	history := second.History(context.Background())
	if len(history) != 1 || history[0].Content != "once" {
		t.Fatalf("redelivery duplicated model history: %+v", history)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.records) != 1 {
		t.Fatalf("redelivery duplicated durable history: %d records", len(store.records))
	}
}

func TestStaleCompactionCannotOverwriteAcceptedHistory(t *testing.T) {
	strategy := BlockingCompaction{entered: make(chan struct{}), release: make(chan struct{})}
	manager := New("thread-1", nil, strategy, nil)
	managerAddHistoryErr := manager.AddHistory(context.Background(), "run-1", schema.UserMessage("old"))
	if managerAddHistoryErr != nil {
		t.Fatal(managerAddHistoryErr)
	}
	result := make(chan error, 1)
	go func() {
		_, err := manager.Compact(context.Background(), "compact-1")
		result <- err
	}()
	<-strategy.entered
	addHistoryErr := manager.AddHistory(context.Background(), "run-2", schema.UserMessage("fresh"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	close(strategy.release)
	checkErr := <-result
	if checkErr != nil {
		t.Fatal(checkErr)
	}
	history := manager.History(context.Background())
	if len(history) != 2 || history[0].Content != "old" || history[1].Content != "fresh" {
		t.Fatalf("stale compaction replaced accepted history: %+v", history)
	}
}
