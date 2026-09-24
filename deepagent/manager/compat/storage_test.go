package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	"github.com/redis/go-redis/v9"
)

func TestProductionRequiresExplicitSharedStores(t *testing.T) {
	if _, e := New(ctx, Config{Namespace: "test"}); e == nil {
		t.Fatal("production silently accepted absent stores")
	}
}

func localRedis(t *testing.T) *redis.Client {
	t.Helper()
	if addr := os.Getenv("DEEPAGENT_TEST_REDIS_ADDR"); addr != "" {
		client := redis.NewClient(&redis.Options{Addr: addr})
		t.Cleanup(func() { client.Close() })
		return client
	}
	if os.Getenv("DEEPAGENT_TEST_LOCAL_REDIS") != "1" {
		t.Skip("set DEEPAGENT_TEST_REDIS_ADDR or DEEPAGENT_TEST_LOCAL_REDIS=1")
	}
	exe, e := exec.LookPath("redis-server")
	if e != nil {
		t.Skip("redis-server is not installed")
	}
	dir, e := os.MkdirTemp("/private/tmp", "deepagent-redis-")
	if e != nil {
		t.Fatal(e)
	}
	socket := filepath.Join(dir, "redis.sock")
	cmd := exec.Command(exe, "--port", "0", "--unixsocket", socket, "--save", "", "--appendonly", "no")
	if e = cmd.Start(); e != nil {
		os.RemoveAll(dir)
		t.Fatal(e)
	}
	client := redis.NewClient(&redis.Options{Network: "unix", Addr: socket})
	t.Cleanup(func() { client.Close(); cmd.Process.Kill(); cmd.Wait(); os.RemoveAll(dir) })
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if client.Ping(ctx).Err() == nil {
			return client
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("redis failed to become ready")
	return nil
}

func TestRedisRevisionDeliveryRecoveryAndPubsub(t *testing.T) {
	rc := localRedis(t)
	life, stop := context.WithCancel(ctx)
	defer stop()
	s := &sqlStore{redis: rc, namespace: "ns", prefix: "deepagent:" + protocol.NewID("redis-test") + ":", ctx: life, stop: stop}
	m, th, _ := setup(t)
	r, e := m.store.load(ctx, th.ID)
	if e != nil {
		t.Fatal(e)
	}
	first, e := s.deliver(ctx, r)
	if e != nil || len(first) != 1 {
		t.Fatal(first, e)
	}
	cacheKey := fmt.Sprintf("%squeue:%s:%d", s.prefix, keyPart(r.Thread.ID), r.Revision)
	if rc.Exists(ctx, cacheKey).Val() != 1 {
		t.Fatal("Redis delivery cache was not populated")
	}
	if e = rc.Del(ctx, cacheKey).Err(); e != nil {
		t.Fatal(e)
	}
	restored, e := s.deliver(ctx, r)
	if e != nil || len(restored) != 1 || restored[0].ID != first[0].ID {
		t.Fatal(restored, e)
	}
	next := cloneRecord(r)
	next.Revision++
	next.Inputs[0].AcceptedToken = "accepted"
	got, e := s.deliver(ctx, next)
	if e != nil || len(got) != 0 {
		t.Fatal(got, e)
	}
	if _, e = s.deliver(ctx, r); e != nil {
		t.Fatal(e)
	}
	got, e = s.deliver(ctx, next)
	if e != nil || len(got) != 0 {
		t.Fatalf("old snapshot overwrote receipt: %v %v", got, e)
	}
	sub, e := s.subscribe(ctx, "session")
	if e != nil {
		t.Fatal(e)
	}
	defer sub.Close()
	event := protocol.Event{ID: "event", Namespace: "ns", SessionID: "session", Kind: protocol.EventText, Text: "live"}
	if e = s.publish(ctx, event); e != nil {
		t.Fatal(e)
	}
	select {
	case got := <-sub.Events:
		if got.ID != event.ID {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("pubsub missed event")
	}
}

// Set DEEPAGENT_TEST_MYSQL_DSN and DEEPAGENT_TEST_REDIS_ADDR to run the actual
// cross-client storage contract. It creates and deletes only its unique namespaces.
func TestMySQLRedisIntegration(t *testing.T) {
	dsn, addr := os.Getenv("DEEPAGENT_TEST_MYSQL_DSN"), os.Getenv("DEEPAGENT_TEST_REDIS_ADDR")
	if dsn == "" || addr == "" {
		t.Skip("set DEEPAGENT_TEST_MYSQL_DSN and DEEPAGENT_TEST_REDIS_ADDR")
	}
	namespace := protocol.NewID("manager-test")
	config := Config{Namespace: namespace, MySQLDSN: dsn, RedisAddr: addr}
	a, e := New(ctx, config)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := New(ctx, config)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	config.Namespace = strings.ToUpper(namespace)
	other, e := New(ctx, config)
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	s := a.store.(*sqlStore)
	defer func() {
		for _, table := range []any{&eventRow{}, &threadRow{}, &checkpointRow{}, &memoryRow{}, &inputRow{}, &historyRow{}, &namespaceRow{}} {
			if e := s.db.Where("namespace IN ?", []string{namespace, config.Namespace}).Delete(table).Error; e != nil {
				t.Error(e)
			}
		}
	}()
	job, e := a.ClaimMemory(ctx, "user/source", "extractor", 200*time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.ClaimMemory(ctx, job.Key, "competitor", time.Minute); !errors.Is(e, api.ErrConflict) {
		t.Fatal(e)
	}
	if e = a.CompleteMemory(ctx, job, "source-v1", []byte(`{"memory":"durable"}`)); e != nil {
		t.Fatal(e)
	}
	artifact, e := b.GetMemory(ctx, job.Key)
	if e != nil || artifact.Version != "source-v1" {
		t.Fatal(artifact, e)
	}
	if _, e = other.GetMemory(ctx, job.Key); !errors.Is(e, api.ErrNotFound) {
		t.Fatal(e)
	}
	nextJob, e := b.ClaimMemory(ctx, job.Key, "extractor-2", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = a.CompleteMemory(ctx, job, "stale", nil); !errors.Is(e, api.ErrPermitLost) {
		t.Fatal(e)
	}
	if _, e = b.RenewMemory(ctx, nextJob, time.Minute); e != nil {
		t.Fatal(e)
	}
	if e = b.ReleaseMemory(ctx, nextJob); e != nil {
		t.Fatal(e)
	}
	artifacts, e := a.ListMemory(ctx, "user/", 100, 0)
	if e != nil || len(artifacts) != 1 {
		t.Fatal(artifacts, e)
	}

	input := protocol.Input{Kind: protocol.InputUser, Text: "persist and recover"}
	thread, e := a.CreateThread(ctx, api.CreateThreadRequest{Input: &input, WorkDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	var scheduling threadRow
	if e = s.db.Where("namespace = ? AND id = ?", namespace, thread.ID).Take(&scheduling).Error; e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(scheduling.Body), "persist and recover") {
		t.Fatal("input journal embedded in scheduling row")
	}
	if _, e = other.GetThread(ctx, thread.ID); !errors.Is(e, api.ErrNotFound) {
		t.Fatal(e)
	}
	threads, e := b.ListSessionThreads(ctx, thread.SessionID, 10, 0)
	if e != nil || len(threads) != 1 {
		t.Fatal(threads, e)
	}
	var wg sync.WaitGroup
	claims := make(chan api.Claim, 2)
	for i, m := range []*Manager{a, b} {
		wg.Add(1)
		go func(i int, m *Manager) {
			defer wg.Done()
			c, e := m.ClaimThread(ctx, thread.ID, []string{"a", "b"}[i], 200*time.Millisecond)
			if e == nil {
				claims <- c
			} else if !errors.Is(e, api.ErrConflict) {
				t.Error(e)
			}
		}(i, m)
	}
	wg.Wait()
	close(claims)
	if len(claims) != 1 {
		t.Fatalf("claim winners: %d", len(claims))
	}
	claim := <-claims
	if e = a.ConfirmInputDelivery(ctx, claim.Permit, claim.Inputs[0].ID); e != nil {
		t.Fatal(e)
	}
	if _, e = a.SaveHistory(ctx, claim.Permit, api.History{Messages: []byte(`[{"role":"user","content":"saved"}]`), Rollout: []byte(`[{"Type":"message","ThreadID":"thread","Seq":1,"Message":{"role":"user","content":"saved"}}]`)}); e != nil {
		t.Fatal(e)
	}
	if e = a.PutThreadCheckpoint(ctx, claim.Permit, "checkpoint", []byte("checkpoint payload")); e != nil {
		t.Fatal(e)
	}
	// Reopen another independent client and wait for the former execution permit.
	time.Sleep(220 * time.Millisecond)
	candidate, e := b.ScanRunnableThreads(ctx, 10)
	if e != nil || len(candidate) != 1 {
		t.Fatal(candidate, e)
	}
	recovered, e := b.ClaimThread(ctx, thread.ID, "replacement", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if len(recovered.Inputs) != 1 || recovered.Inputs[0].ID != claim.Inputs[0].ID {
		t.Fatal(recovered.Inputs)
	}
	if _, e = a.PublishEvent(ctx, claim.Permit, protocol.Event{Kind: protocol.EventText}); !errors.Is(e, api.ErrPermitLost) {
		t.Fatal(e)
	}
	if e = a.PutThreadCheckpoint(ctx, claim.Permit, "checkpoint", []byte("stale overwrite")); !errors.Is(e, api.ErrPermitLost) {
		t.Fatal(e)
	}
	h, e := b.LoadHistory(ctx, thread.ID)
	if e != nil || h.Version != 1 || !bytes.Contains(h.Rollout, []byte(`"Seq":1`)) {
		t.Fatal(h, e)
	}
	if _, e = b.SaveHistory(ctx, recovered.Permit, api.History{}); !errors.Is(e, api.ErrConflict) {
		t.Fatal(e)
	}
	cp, e := b.GetCheckpoint(ctx, "checkpoint")
	if e != nil || string(cp) != "checkpoint payload" {
		t.Fatal(string(cp), e)
	}
	if _, e = other.GetCheckpoint(ctx, "checkpoint"); !errors.Is(e, api.ErrNotFound) {
		t.Fatal(e)
	}
	sub, e := a.SubscribeSession(ctx, thread.SessionID)
	if e != nil {
		t.Fatal(e)
	}
	defer sub.Close()
	event, e := b.PublishEvent(ctx, recovered.Permit, protocol.Event{Kind: protocol.EventRunCompleted, RunID: "run", MessageIDs: []string{claim.Inputs[0].ID}})
	if e != nil {
		t.Fatal(e)
	}
	select {
	case live := <-sub.Events:
		durable, e := a.ListEvents(ctx, api.EventFilter{SessionID: thread.SessionID})
		if e != nil || len(durable) != 1 || durable[0].ID != live.ID {
			t.Fatal(durable, e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("missing live event")
	}
	dup, e := b.PublishEvent(ctx, recovered.Permit, event)
	if e != nil || dup.Sequence != event.Sequence {
		t.Fatal(dup, e)
	}
	if e = b.ReleaseThread(ctx, recovered.Permit, api.Release{}); e != nil {
		t.Fatal(e)
	}
	state, e := a.GetThread(ctx, thread.ID)
	if e != nil || state.State != api.Idle {
		t.Fatal(state, e)
	}
	sources, e := a.ListMemorySources(ctx, 10, 0)
	if e != nil || len(sources) != 1 || sources[0].ThreadID != thread.ID {
		t.Fatal(sources, e)
	}
	if isolated, e := other.ListMemorySources(ctx, 10, 0); e != nil || len(isolated) != 0 {
		t.Fatal(isolated, e)
	}
	input2, e := a.SubmitInput(ctx, thread.ID, protocol.Input{Kind: protocol.InputUser, Text: "approval"})
	if e != nil {
		t.Fatal(e)
	}
	blockedClaim, e := b.ClaimThread(ctx, thread.ID, "blocker", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = b.ConfirmInputDelivery(ctx, blockedClaim.Permit, input2.ID); e != nil {
		t.Fatal(e)
	}
	block := &protocol.Block{RunID: "approval-run", CheckpointID: "approval-cp", InterruptID: "approval-int"}
	if _, e = b.PublishEvent(ctx, blockedClaim.Permit, protocol.Event{Kind: protocol.EventBlocked, RunID: block.RunID, MessageIDs: []string{input2.ID}, Block: block}); e != nil {
		t.Fatal(e)
	}
	if e = b.ReleaseThread(ctx, blockedClaim.Permit, api.Release{Block: block}); e != nil {
		t.Fatal(e)
	}
	resume, e := a.ResumeFromBlock(ctx, thread.ID, protocol.Input{Kind: protocol.InputResume, Resume: &protocol.Resume{RunID: block.RunID, CheckpointID: block.CheckpointID, InterruptID: block.InterruptID, Approved: true}})
	if e != nil {
		t.Fatal(e)
	}
	resumed, e := b.ClaimThread(ctx, thread.ID, "resumer", time.Minute)
	if e != nil || len(resumed.Inputs) != 1 || resumed.Inputs[0].ID != resume.ID {
		t.Fatal(resumed, e)
	}
	if e = a.RequestThreadClose(ctx, thread.ID); e != nil {
		t.Fatal(e)
	}
	if e = b.ConfirmThreadClosed(ctx, resumed.Permit); e != nil {
		t.Fatal(e)
	}
	sources, e = a.ListMemorySources(ctx, 10, 0)
	if e != nil || len(sources) != 1 {
		t.Fatal(sources, e)
	}
	// A never-delivered request gets its terminal event in the cancellation transaction.
	pendingInput := protocol.Input{ID: protocol.NewID("pending"), Kind: protocol.InputUser, Text: "cancel before claim"}
	pendingThread, e := a.CreateThread(ctx, api.CreateThreadRequest{Input: &pendingInput})
	if e != nil {
		t.Fatal(e)
	}
	if e = a.Cancel(ctx, pendingThread.ID, pendingInput.ID); e != nil {
		t.Fatal(e)
	}
	cancelled, e := b.ListEvents(ctx, api.EventFilter{ThreadID: pendingThread.ID})
	if e != nil || len(cancelled) != 1 || cancelled[0].Kind != protocol.EventRunCancelled || len(cancelled[0].MessageIDs) != 1 || cancelled[0].MessageIDs[0] != pendingInput.ID || cancelled[0].RunID == "" {
		t.Fatal(cancelled, e)
	}
	// Reopen an old-layout row, then lazily migrate its accepted receipt and history.
	legacyID := protocol.NewID("legacy-thread")
	legacyInput := protocol.Input{ID: protocol.NewID("legacy-input"), ThreadID: legacyID, SessionID: thread.SessionID, Kind: protocol.InputUser, Text: "legacy receipt"}
	legacy := &record{Thread: api.Thread{ID: legacyID, Namespace: namespace, SessionID: thread.SessionID, State: api.Running, CreatedAt: time.Now().UTC()}, Permit: api.Permit{ThreadID: legacyID, Token: "dead-worker", WorkerID: "dead", ExpiresAt: time.Now().Add(-time.Minute)}, Inputs: []delivery{{Input: legacyInput, AcceptedToken: "dead-worker"}}, History: api.History{Version: 7, Messages: []byte(`[{"role":"user","content":"legacy history"}]`)}}
	legacyRow, e := encode(legacy)
	if e != nil {
		t.Fatal(e)
	}
	legacyRow.Body, e = json.Marshal(legacy)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.db.Create(&legacyRow).Error; e != nil {
		t.Fatal(e)
	}
	historical, e := b.LoadHistory(ctx, legacyID)
	if e != nil || historical.Version != 7 {
		t.Fatal(historical, e)
	}
	takeover, e := b.ClaimThread(ctx, legacyID, "migrator", time.Minute)
	if e != nil || len(takeover.Inputs) != 1 || takeover.Inputs[0].ID != legacyInput.ID {
		t.Fatal(takeover, e)
	}
	historical, e = a.LoadHistory(ctx, legacyID)
	if e != nil || historical.Version != 7 || !strings.Contains(string(historical.Messages), "legacy history") {
		t.Fatal(historical, e)
	}
	var migrated threadRow
	if e = s.db.Where("namespace = ? AND id = ?", namespace, legacyID).Take(&migrated).Error; e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(migrated.Body), "legacy receipt") || strings.Contains(string(migrated.Body), "legacy history") {
		t.Fatal("legacy row not normalized")
	}
	var receiptCount, historyCount int64
	s.db.Model(&inputRow{}).Where("namespace = ? AND thread_id = ?", namespace, legacyID).Count(&receiptCount)
	s.db.Model(&historyRow{}).Where("namespace = ? AND thread_id = ?", namespace, legacyID).Count(&historyCount)
	if receiptCount != 1 || historyCount != 1 {
		t.Fatalf("migration lost durable records: receipts=%d histories=%d", receiptCount, historyCount)
	}
}

func TestSchedulingPayloadExcludesInputsAndHistory(t *testing.T) {
	m, th, _ := setup(t)
	r, e := m.store.load(ctx, th.ID)
	if e != nil {
		t.Fatal(e)
	}
	r.History = api.History{Version: 1, Messages: []byte(`[{"content":"large-history"}]`)}
	row, e := encode(r)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(row.Body), "hello") || strings.Contains(string(row.Body), "large-history") {
		t.Fatal("scheduling payload embeds journal/history")
	}
}
