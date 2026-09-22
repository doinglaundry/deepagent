package distributed

import (
	"context"
	"eino-cli/deepagent/core/compact"
	"eino-cli/deepagent/manager/api"
	"encoding/json"
	"github.com/cloudwego/eino/schema"
	"testing"
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
	m := &storeManager{history: api.History{Version: 4, Messages: data}}
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
