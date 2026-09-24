package conversation

import (
	"context"
	"errors"
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
	if err := c.AddHistory(ctx, "run", schema.UserMessage("hello")); err == nil {
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
	return &CompactionResult{Compact: &CompactRecord{Summary: s, CompactStrategyID: "test"}, Rebuilt: append([]*Message{s}, m[len(m)-1:]...)}, nil
}
func (*testCompactor) Resume(_ context.Context, r *CompactRecord, post []*Message) (*ResumeResult, error) {
	return &ResumeResult{Rebuilt: append([]*Message{r.Summary}, post...)}, nil
}
func TestContext_ReloadEqualsCompactedContext(t *testing.T) {
	ctx := context.Background()
	store := &testStore{}
	c := New("thread", store, &testCompactor{}, nil)
	if err := c.AddHistory(ctx, "run", schema.UserMessage("older"), schema.UserMessage("retain")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Compact(ctx, "run"); err != nil {
		t.Fatal(err)
	}
	if err := c.AddHistory(ctx, "run", schema.UserMessage("later")); err != nil {
		t.Fatal(err)
	}
	restored := New("thread", store, &testCompactor{}, nil)
	if err := restored.ReloadHistory(ctx); err != nil {
		t.Fatal(err)
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
	if err := c.AddHistory(ctx, "run", schema.UserMessage("before")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := c.Compact(ctx, "run"); done <- err }()
	<-compactor.started
	if err := c.AddHistory(ctx, "run", schema.UserMessage("new input")); err != nil {
		t.Fatal(err)
	}
	close(compactor.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	h := c.History(ctx)
	if len(h) != 2 || h[0].Content != "before" || h[1].Content != "new input" {
		t.Fatalf("stale summary overwrote input: %v", h)
	}
	if len(store.records) != 2 {
		t.Fatal("stale compact was persisted")
	}
}
