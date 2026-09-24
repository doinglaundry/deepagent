package distributed

import (
	"context"
	"eino-cli/deepagent/core/compact"
	"eino-cli/deepagent/manager/api"
	"encoding/json"
	"errors"
	"github.com/cloudwego/eino/schema"
	"testing"
	"time"
)

type storeManager struct {
	api.Manager
	history api.History
	permit  api.Permit
}

func (m *storeManager) LoadHistory(context.Context, string) (api.History, error) {
	return m.history, nil
}
func (m *storeManager) SaveHistory(_ context.Context, p api.Permit, h api.History) (api.History, error) {
	if h.Version != m.history.Version {
		return api.History{}, api.ErrConflict
	}
	m.permit = p
	h.Version++
	m.history = h
	return h, nil
}
func TestHistoryTracksCASVersionAndUsesPermit(t *testing.T) {
	data, _ := json.Marshal([]*schema.Message{schema.UserMessage("old")})
	m := &storeManager{history: api.History{Version: 4, Messages: data, Rollout: json.RawMessage(`null`)}}
	h := NewHistory(context.Background(), m, api.Permit{ThreadID: "thread", Token: "token"})
	messages, err := h.Load(context.Background())
	if err != nil || len(messages) != 1 {
		t.Fatalf("load %v %v", messages, err)
	}
	for i := 0; i < 2; i++ {
		if err = h.Save(context.Background(), []*schema.Message{schema.UserMessage("new")}); err != nil {
			t.Fatal(err)
		}
	}
	if m.history.Version != 6 || m.permit.Token != "token" {
		t.Fatalf("version %d permit %+v", m.history.Version, m.permit)
	}
}

func TestCompactionRecordAndHistoryPersistTogether(t *testing.T) {
	m := &storeManager{history: api.History{Version: 1}}
	h := NewHistory(context.Background(), m, api.Permit{ThreadID: "thread", Token: "token"})
	if _, err := h.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := h.SaveCompacted(context.Background(), []*schema.Message{schema.SystemMessage("summary")}, compact.Record{Removed: 12, Summary: "summary"}); err != nil {
		t.Fatal(err)
	}
	var records []compact.Record
	if err := json.Unmarshal(m.history.Compactions, &records); err != nil || len(records) != 1 || records[0].Removed != 12 {
		t.Fatalf("records %v err %v", records, err)
	}
}

type cancellationHistoryManager struct{ api.Manager }

func (*cancellationHistoryManager) LoadHistory(ctx context.Context, _ string) (api.History, error) {
	return api.History{}, ctx.Err()
}
func TestHistoryLoadHonorsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h := NewHistory(context.Background(), &cancellationHistoryManager{}, api.Permit{ThreadID: "thread"})
	if _, err := h.Load(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation ignored: %v", err)
	}
}
func TestHistoryMalformedLoadDoesNotAdvanceVersion(t *testing.T) {
	m := &storeManager{history: api.History{Version: 4, Messages: json.RawMessage(`[]`)}}
	h := NewHistory(context.Background(), m, api.Permit{ThreadID: "thread"})
	if _, err := h.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.history = api.History{Version: 5, Messages: json.RawMessage(`invalid`)}
	if _, err := h.Load(context.Background()); err == nil {
		t.Fatal("malformed history accepted")
	}
	if h.history.Version != 4 {
		t.Fatalf("failed read advanced version: %d", h.history.Version)
	}
}

type waitingHistoryManager struct {
	api.Manager
	started chan struct{}
}

func (m *waitingHistoryManager) LoadHistory(ctx context.Context, _ string) (api.History, error) {
	close(m.started)
	<-ctx.Done()
	return api.History{}, ctx.Err()
}
func TestHistoryLoadStopsWhenClaimIsCanceled(t *testing.T) {
	claim, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := &waitingHistoryManager{started: make(chan struct{})}
	h := NewHistory(claim, m, api.Permit{ThreadID: "thread"})
	done := make(chan error, 1)
	go func() { _, err := h.Load(context.Background()); done <- err }()
	select {
	case <-m.started:
	case <-time.After(time.Second):
		t.Fatal("history read did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("claim cancellation did not stop read")
	}
}
