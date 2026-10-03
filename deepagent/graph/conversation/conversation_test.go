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

func (historyStore *testStore) Append(_ context.Context, historyRecord *HistoryRecord) error {
	if historyStore.fail {
		return errors.New("store failed")
	}
	historyRecord.Seq = int64(len(historyStore.records) + 1)
	historyStore.records = append(historyStore.records, historyRecord)
	return nil
}
func (historyStore *testStore) List(_ context.Context, query ListQuery) ([]*HistoryRecord, error) {
	var out []*HistoryRecord
	for _, historyRecord := range historyStore.records {
		if query.AfterID != nil && historyRecord.Seq <= *query.AfterID {
			continue
		}
		out = append(out, historyRecord)
		if query.Limit > 0 && len(out) >= query.Limit {
			break
		}
	}
	return out, nil
}
func TestContext_PersistFailureDoesNotChangeHistory(t *testing.T) {
	ctx := context.Background()
	store := &testStore{fail: true}
	conversation := New("thread", store, nil, nil)
	err := conversation.AddHistory(ctx, "run", schema.UserMessage("hello"))
	if err == nil {
		t.Fatal("expected persistence failure")
	}
	if len(conversation.GetHistory(ctx)) != 0 {
		t.Fatal("failed write appeared in history")
	}
}

type testCompactor struct {
	started chan struct{}
	release chan struct{}
}

func (*testCompactor) GetID() string { return "test" }
func (compactor *testCompactor) Compact(ctx context.Context, messages []*Message) (*CompactionResult, error) {
	if compactor.started != nil {
		close(compactor.started)
		select {
		case <-compactor.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	summary := schema.SystemMessage("summary")
	return &CompactionResult{Summary: summary, Rebuilt: append([]*Message{summary}, messages[len(messages)-1:]...)}, nil
}
func TestContext_ReloadEqualsCompactedContext(t *testing.T) {
	ctx := context.Background()
	store := &testStore{}
	conversation := New("thread", store, &testCompactor{}, nil)
	err := conversation.AddHistory(ctx, "run", schema.UserMessage("older"), schema.UserMessage("retain"))
	if err != nil {
		t.Fatal(err)
	}
	_, compactErr := conversation.Compact(ctx, "run")
	if compactErr != nil {
		t.Fatal(compactErr)
	}
	addHistoryErr := conversation.AddHistory(ctx, "run", schema.UserMessage("later"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	restored := New("thread", store, &testCompactor{}, nil)
	reloadHistoryErr := restored.ReloadHistory(ctx)
	if reloadHistoryErr != nil {
		t.Fatal(reloadHistoryErr)
	}
	messages := restored.GetHistory(ctx)
	if len(messages) != 3 || messages[0].Content != "summary" || messages[1].Content != "retain" || messages[2].Content != "later" {
		t.Fatalf("retained context lost: %v", messages)
	}
}
func TestContext_StaleCompactionCannotOverwriteNewInput(t *testing.T) {
	ctx := context.Background()
	compactor := &testCompactor{started: make(chan struct{}), release: make(chan struct{})}
	store := &testStore{}
	conversation := New("thread", store, compactor, nil)
	cAddHistoryErr := conversation.AddHistory(ctx, "run", schema.UserMessage("before"))
	if cAddHistoryErr != nil {
		t.Fatal(cAddHistoryErr)
	}
	done := make(chan error, 1)
	go func() { _, err := conversation.Compact(ctx, "run"); done <- err }()
	<-compactor.started
	addHistoryErr := conversation.AddHistory(ctx, "run", schema.UserMessage("new input"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	close(compactor.release)
	checkErr := <-done
	if checkErr != nil {
		t.Fatal(checkErr)
	}
	history := conversation.GetHistory(ctx)
	if len(history) != 2 || history[0].Content != "before" || history[1].Content != "new input" {
		t.Fatalf("stale summary overwrote input: %v", history)
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

func (BlockingCompaction) GetID() string { return "blocking" }

func (compactor BlockingCompaction) Compact(context.Context, []*Message) (*CompactionResult, error) {
	close(compactor.entered)
	<-compactor.release
	summary := schema.SystemMessage("stale summary")
	return &CompactionResult{
		Summary: summary,
		Rebuilt: []*Message{summary},
	}, nil
}

func (historyStore *DedupHistoryStore) Append(_ context.Context, record *HistoryRecord) error {
	historyStore.mu.Lock()
	defer historyStore.mu.Unlock()
	if historyStore.seen == nil {
		historyStore.seen = make(map[int64]struct{})
	}
	_, exists := historyStore.seen[record.MessageID]
	if exists {
		return nil
	}
	historyStore.seen[record.MessageID] = struct{}{}
	copy := *record
	copy.Seq = int64(len(historyStore.records) + 1)
	historyStore.records = append(historyStore.records, &copy)
	return nil
}

func (historyStore *DedupHistoryStore) List(_ context.Context, query ListQuery) ([]*HistoryRecord, error) {
	historyStore.mu.Lock()
	defer historyStore.mu.Unlock()
	result := append([]*HistoryRecord(nil), historyStore.records...)
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
	history := second.GetHistory(context.Background())
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
	conversation := New("thread-1", nil, strategy, nil)
	managerAddHistoryErr := conversation.AddHistory(context.Background(), "run-1", schema.UserMessage("old"))
	if managerAddHistoryErr != nil {
		t.Fatal(managerAddHistoryErr)
	}
	result := make(chan error, 1)
	go func() {
		_, err := conversation.Compact(context.Background(), "compact-1")
		result <- err
	}()
	<-strategy.entered
	addHistoryErr := conversation.AddHistory(context.Background(), "run-2", schema.UserMessage("fresh"))
	if addHistoryErr != nil {
		t.Fatal(addHistoryErr)
	}
	close(strategy.release)
	checkErr := <-result
	if checkErr != nil {
		t.Fatal(checkErr)
	}
	history := conversation.GetHistory(context.Background())
	if len(history) != 2 || history[0].Content != "old" || history[1].Content != "fresh" {
		t.Fatalf("stale compaction replaced accepted history: %+v", history)
	}
}
