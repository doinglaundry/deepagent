package conversation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	dalmodel "eino-cli/deepagent/dal/model"
	"github.com/cloudwego/eino/schema"
)

type testStore struct {
	records []*dalmodel.ConversationEntry
	fail    bool
}

func TestConversationEntryIDAllocationFailureDoesNotPublishMessage(t *testing.T) {
	failure := errors.New("id allocation failed")
	conversation := New("thread", &testStore{}, nil, nil, WithEntryID(func(context.Context, string, string, *schema.Message) (int64, error) { return 0, failure }))
	err := conversation.AddHistory(context.Background(), "run", schema.UserMessage("must not appear"))
	if !errors.Is(err, failure) || len(conversation.GetHistory(context.Background())) != 0 {
		t.Fatalf("allocation failure was swallowed: %v", err)
	}
}

func (conversationRepository *testStore) Append(_ context.Context, conversationEntry *dalmodel.ConversationEntry) error {
	if conversationRepository.fail {
		return errors.New("store failed")
	}
	conversationEntry.Seq = int64(len(conversationRepository.records) + 1)
	conversationRepository.records = append(conversationRepository.records, conversationEntry)
	return nil
}
func (conversationRepository *testStore) LoadAfter(_ context.Context, threadID string, sequence int64, limit int) ([]*dalmodel.ConversationEntry, error) {
	var out []*dalmodel.ConversationEntry
	for _, conversationEntry := range conversationRepository.records {
		if conversationEntry.ThreadID != threadID || conversationEntry.Seq <= sequence {
			continue
		}
		out = append(out, conversationEntry)
		if limit > 0 && len(out) >= limit {
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
func (compactor *testCompactor) Compact(ctx context.Context, messages []*schema.Message) ([]*schema.Message, error) {
	if compactor.started != nil {
		close(compactor.started)
		select {
		case <-compactor.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	summary := schema.SystemMessage("summary")
	return append([]*schema.Message{summary}, messages[len(messages)-1:]...), nil
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

type DedupConversationRepository struct {
	mu      sync.Mutex
	records []*dalmodel.ConversationEntry
	seen    map[int64]struct{}
}

type BlockingCompaction struct {
	entered chan struct{}
	release chan struct{}
}

func (BlockingCompaction) GetID() string { return "blocking" }

func (compactor BlockingCompaction) Compact(context.Context, []*schema.Message) ([]*schema.Message, error) {
	close(compactor.entered)
	<-compactor.release
	summary := schema.SystemMessage("stale summary")
	return []*schema.Message{summary}, nil
}

func (conversationRepository *DedupConversationRepository) Append(_ context.Context, record *dalmodel.ConversationEntry) error {
	conversationRepository.mu.Lock()
	defer conversationRepository.mu.Unlock()
	if conversationRepository.seen == nil {
		conversationRepository.seen = make(map[int64]struct{})
	}
	_, exists := conversationRepository.seen[record.MessageID]
	if exists {
		return nil
	}
	conversationRepository.seen[record.MessageID] = struct{}{}
	copy := *record
	record.Seq = int64(len(conversationRepository.records) + 1)
	copy.Seq = record.Seq
	conversationRepository.records = append(conversationRepository.records, &copy)
	return nil
}

func (conversationRepository *DedupConversationRepository) LoadAfter(_ context.Context, threadID string, sequence int64, limit int) ([]*dalmodel.ConversationEntry, error) {
	conversationRepository.mu.Lock()
	defer conversationRepository.mu.Unlock()
	var records []*dalmodel.ConversationEntry
	for _, record := range conversationRepository.records {
		if record.ThreadID != threadID || record.Seq <= sequence {
			continue
		}
		records = append(records, record)
		if limit > 0 && len(records) >= limit {
			break
		}
	}
	return records, nil
}

func TestHistoryRedeliveryUsesDurableMessageIdentity(t *testing.T) {
	store := &DedupConversationRepository{}
	provider := func(context.Context, string, string, *schema.Message) (int64, error) { return 42, nil }
	first := New("thread-1", store, nil, nil, WithEntryID(provider))
	err := first.AddHistory(context.Background(), "run-1", schema.UserMessage("once"))
	if err != nil {
		t.Fatal(err)
	}

	second := New("thread-1", store, nil, nil, WithEntryID(provider))
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

func TestReloadHistoryCrossesPageBoundary(t *testing.T) {
	store := &testStore{}
	first := New("thread", store, nil, nil)
	messages := make([]*schema.Message, 205)
	for index := range messages {
		messages[index] = schema.UserMessage(fmt.Sprintf("message %d", index))
	}
	err := first.AddHistory(context.Background(), "run", messages...)
	if err != nil {
		t.Fatal(err)
	}
	restored := New("thread", store, nil, nil)
	err = restored.ReloadHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	history := restored.GetHistory(context.Background())
	if len(history) != 205 || history[199].Content != "message 199" || history[204].Content != "message 204" || restored.SnapshotContext().HistoryCursor != 205 {
		t.Fatal("reload lost messages or sequence at the page boundary")
	}
}
