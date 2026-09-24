package distributed

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"eino-cli/deepagent/core/runtime/agentthread"
	"eino-cli/deepagent/manager/api"
	"github.com/cloudwego/eino/schema"
)

func TestRolloutMigratesLegacyAndPreservesOrderAcrossReopen(t *testing.T) {
	ctx := context.Background()
	m := &storeManager{history: api.History{Version: 3, Rollout: json.RawMessage(`null`), Messages: json.RawMessage(`[{"role":"user","content":"old"}]`)}}
	permit := api.Permit{ThreadID: "thread", Token: "token"}
	h := NewHistory(ctx, m, permit)
	rec := &agentthread.HistoryRecord{ThreadID: "thread", RunID: "run", Type: "message", UniqueKey: "new", Message: schema.AssistantMessage("new", nil)}
	if err := h.Append(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if rec.Seq != 2 || rec.MessageID != 2 || m.history.Version != 4 || m.permit.Token != "token" {
		t.Fatalf("record=%+v history=%+v", rec, m.history)
	}
	reopened := NewHistory(ctx, m, permit)
	retry := *rec
	retry.Seq = 0
	if err := reopened.Append(ctx, &retry); err != nil {
		t.Fatal(err)
	}
	if retry.Seq != 2 || m.history.Version != 4 {
		t.Fatal("redelivery wrote another record")
	}
	after := int64(1)
	rows, err := reopened.List(ctx, agentthread.ListQuery{ThreadID: "thread", AfterID: &after, Limit: 1})
	if err != nil || len(rows) != 1 || rows[0].Message.Content != "new" {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	before := int64(3)
	rows, err = reopened.List(ctx, agentthread.ListQuery{ThreadID: "thread", BeforeID: &before, Order: agentthread.ListOrderDESC})
	if err != nil || len(rows) != 2 || rows[0].Seq != 2 || rows[1].Seq != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	if err := reopened.Save(ctx, []*schema.Message{schema.UserMessage("overwrite")}); err == nil {
		t.Fatal("legacy writer overwrote rollout")
	}
	rows[0].Message.Content = "caller mutation"
	again, err := reopened.List(ctx, agentthread.ListQuery{ThreadID: "thread", RunID: "run"})
	if err != nil || len(again) != 1 || again[0].Message.Content != "new" {
		t.Fatal("returned records alias storage")
	}
}
func TestRolloutCompactionProjectionAndPostCompactAppend(t *testing.T) {
	ctx := context.Background()
	m := &storeManager{}
	h := NewHistory(ctx, m, api.Permit{ThreadID: "thread"})
	for _, text := range []string{"one", "two"} {
		if err := h.Append(ctx, &agentthread.HistoryRecord{ThreadID: "thread", Type: "message", Message: schema.UserMessage(text)}); err != nil {
			t.Fatal(err)
		}
	}
	summary := schema.SystemMessage("summary")
	raw, _ := json.Marshal(agentthread.CompactSnapshot{Version: 1, CoveredSeq: 2, Summary: summary, Retained: []*schema.Message{schema.UserMessage("two")}})
	rec := &agentthread.HistoryRecord{ThreadID: "thread", Type: "compact", Message: summary, Ext: &agentthread.HistoryRecordExtend{CompactStrategyID: "core_snapshot_v1", CompactStrategyPayload: string(raw)}}
	if err := h.Append(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := h.Append(ctx, &agentthread.HistoryRecord{ThreadID: "thread", Type: "message", Message: schema.UserMessage("three")}); err != nil {
		t.Fatal(err)
	}
	var effective []*schema.Message
	if err := json.Unmarshal(m.history.Messages, &effective); err != nil {
		t.Fatal(err)
	}
	if len(effective) != 3 || effective[0].Content != "summary" || effective[2].Content != "three" {
		t.Fatalf("projection=%v", effective)
	}
	rows, err := NewHistory(ctx, m, api.Permit{ThreadID: "thread"}).List(ctx, agentthread.ListQuery{})
	if err != nil || len(rows) != 4 || rows[2].Type != "compact" {
		t.Fatalf("records=%v err=%v", rows, err)
	}
	core := agentthread.New("thread", &agentthread.RunConfig{}, make(chan agentthread.Event, 8), agentthread.ThreadOptions{HistoryStore: NewHistory(ctx, m, api.Permit{ThreadID: "thread"})})
	if err := core.Init(ctx); err != nil {
		t.Fatal(err)
	}
	restored := core.ContextManager().History(ctx)
	if len(restored) != len(effective) {
		t.Fatalf("canonical reload differs: %v", restored)
	}
	for i := range effective {
		if restored[i].Content != effective[i].Content || restored[i].Role != effective[i].Role {
			t.Fatal("projection disagrees with canonical Conversation")
		}
	}
	stale := *rec
	stale.MessageID = 0
	stale.UniqueKey = "new-compact"
	if err := h.Append(ctx, &stale); err == nil {
		t.Fatal("stale compact accepted")
	}
}

type failedRolloutManager struct{ storeManager }

func (m *failedRolloutManager) SaveHistory(context.Context, api.Permit, api.History) (api.History, error) {
	return api.History{}, api.ErrConflict
}
func TestRolloutSaveFailureLeavesRecordAndHistoryUnchanged(t *testing.T) {
	m := &failedRolloutManager{}
	h := NewHistory(context.Background(), m, api.Permit{ThreadID: "thread"})
	rec := &agentthread.HistoryRecord{ThreadID: "thread", Type: "message", Message: schema.UserMessage("new")}
	if err := h.Append(context.Background(), rec); !errors.Is(err, api.ErrConflict) {
		t.Fatal(err)
	}
	if rec.Seq != 0 || rec.MessageID != 0 || len(m.history.Rollout) != 0 || h.history.Version != 0 {
		t.Fatal("failed save mutated state")
	}
}

func TestRolloutRedeliveryUsesTransportIdentityAcrossWorkers(t *testing.T) {
	ctx := context.Background()
	m := &storeManager{}
	permit := api.Permit{ThreadID: "thread"}
	message := func(content string) *schema.Message {
		msg := schema.UserMessage(content)
		msg.Extra = map[string]any{"message_id": "original-input", "sender": "user"}
		return msg
	}
	first := &agentthread.HistoryRecord{ThreadID: "thread", RunID: "first-run", Type: "message", UniqueKey: "first-random-key", Message: message("hello")}
	if err := NewHistory(ctx, m, permit).Append(ctx, first); err != nil {
		t.Fatal(err)
	}
	version := m.history.Version
	retry := &agentthread.HistoryRecord{ThreadID: "thread", RunID: "replacement-run", Type: "message", UniqueKey: "replacement-random-key", Message: message("hello")}
	reopened := NewHistory(ctx, m, permit)
	if err := reopened.Append(ctx, retry); err != nil {
		t.Fatal(err)
	}
	if m.history.Version != version || retry.MessageID != first.MessageID || retry.Seq != first.Seq || retry.UniqueKey != first.UniqueKey {
		t.Fatalf("redelivery changed identity: first=%+v retry=%+v", first, retry)
	}
	rows, err := reopened.List(ctx, agentthread.ListQuery{})
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows=%v err=%v", rows, err)
	}
	conflict := *retry
	conflict.Message = message("different content")
	if err := reopened.Append(ctx, &conflict); err == nil {
		t.Fatal("same input identity accepted different content")
	}
	if m.history.Version != version {
		t.Fatal("conflict mutated history")
	}
}
