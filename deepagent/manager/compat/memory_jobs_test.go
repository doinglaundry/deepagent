package manager

import (
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	"errors"
	"testing"
	"time"
)

func TestMemoryIndependentLeaseAndAtomicProgress(t *testing.T) {
	m := NewMemory("test")
	lease, e := m.ClaimMemory(ctx, "user/alice/source/thread", "worker", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.ClaimMemory(ctx, lease.Key, "competitor", time.Minute); !errors.Is(e, api.ErrConflict) {
		t.Fatal(e)
	}
	if e = m.CompleteMemory(ctx, lease, "history-v1", []byte(`{"raw":"remember"}`)); e != nil {
		t.Fatal(e)
	}
	a, e := m.GetMemory(ctx, lease.Key)
	if e != nil || a.Version != "history-v1" || string(a.Data) != `{"raw":"remember"}` {
		t.Fatal(a, e)
	}
	next, e := m.ClaimMemory(ctx, lease.Key, "next", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.CompleteMemory(ctx, lease, "stale", nil); !errors.Is(e, api.ErrPermitLost) {
		t.Fatal(e)
	}
	still, _ := m.GetMemory(ctx, lease.Key)
	if still.Version != "history-v1" {
		t.Fatal(still)
	}
	if _, e = m.RenewMemory(ctx, next, time.Minute); e != nil {
		t.Fatal(e)
	}
	if e = m.ReleaseMemory(ctx, next); e != nil {
		t.Fatal(e)
	}
	all, e := m.ListMemory(ctx, "user/alice/", 100, 0)
	if e != nil || len(all) != 1 {
		t.Fatal(all, e)
	}
	empty, e := m.ListMemory(ctx, "user/bob/", 100, 0)
	if e != nil || len(empty) != 0 {
		t.Fatal(empty, e)
	}
}
func TestMemoryExpiredLeaseRecovery(t *testing.T) {
	m := NewMemory("test")
	first, e := m.ClaimMemory(ctx, "job", "dead", time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(5 * time.Millisecond)
	next, e := m.ClaimMemory(ctx, "job", "replacement", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if first.Token == next.Token {
		t.Fatal("token was reused")
	}
	if e = m.CompleteMemory(ctx, first, "old", nil); !errors.Is(e, api.ErrPermitLost) {
		t.Fatal(e)
	}
	if e = m.CompleteMemory(ctx, next, protocol.NewID("baseline"), []byte("{}")); e != nil {
		t.Fatal(e)
	}
}
func TestCompactionReceiptDoesNotReplay(t *testing.T) {
	m := NewMemory("test")
	in := protocol.Input{Kind: protocol.InputCompact}
	th, e := m.CreateThread(ctx, api.CreateThreadRequest{Input: &in})
	if e != nil {
		t.Fatal(e)
	}
	c, _ := m.ClaimThread(ctx, th.ID, "w", time.Minute)
	m.ConfirmInputDelivery(ctx, c.Permit, c.Inputs[0].ID)
	if _, e = m.PublishEvent(ctx, c.Permit, protocol.Event{Kind: protocol.EventCompacted, MessageIDs: []string{c.Inputs[0].ID}}); e != nil {
		t.Fatal(e)
	}
	if e = m.ReleaseThread(ctx, c.Permit, api.Release{}); e != nil {
		t.Fatal(e)
	}
	got, _ := m.GetThread(ctx, th.ID)
	if got.State != api.Idle {
		t.Fatalf("compaction replayed: %s", got.State)
	}
}

func TestMemorySourcesFilterBeforePagination(t *testing.T) {
	m, th, in := setup(t)
	if _, e := m.CreateThread(ctx, api.CreateThreadRequest{}); e != nil {
		t.Fatal(e)
	}
	c, _ := m.ClaimThread(ctx, th.ID, "worker", time.Minute)
	if _, e := m.SaveHistory(ctx, c.Permit, api.History{Messages: []byte(`[{"role":"user","content":"remember"}]`)}); e != nil {
		t.Fatal(e)
	}
	sources, e := m.ListMemorySources(ctx, 1, 0)
	if e != nil || len(sources) != 0 {
		t.Fatalf("active source exposed: %v %v", sources, e)
	}
	if _, e = m.PublishEvent(ctx, c.Permit, protocol.Event{Kind: protocol.EventRunCompleted, RunID: "run", MessageIDs: []string{in.ID}}); e != nil {
		t.Fatal(e)
	}
	if e = m.ReleaseThread(ctx, c.Permit, api.Release{}); e != nil {
		t.Fatal(e)
	}
	sources, e = m.ListMemorySources(ctx, 1, 0)
	if e != nil || len(sources) != 1 || sources[0].ThreadID != th.ID || sources[0].History.Version != 1 {
		t.Fatal(sources, e)
	}
	sources, e = m.ListMemorySources(ctx, 1, 1)
	if e != nil || len(sources) != 0 {
		t.Fatal(sources, e)
	}
}
