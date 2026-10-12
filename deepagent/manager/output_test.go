package manager

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	dalcache "eino-cli/deepagent/dal/cache"
	daldb "eino-cli/deepagent/dal/db"
	agentmodel "eino-cli/deepagent/model"

	"github.com/google/uuid"
)

// These tests use an isolated database selected explicitly by the caller.
func releaseTestManager(t *testing.T) (*Manager, *agentmodel.ThreadRecord) {
	t.Helper()
	dsn := os.Getenv("DEEPAGENT_TEST_MYSQL_DSN")
	addr := os.Getenv("DEEPAGENT_TEST_REDIS_ADDR")
	if dsn == "" || addr == "" {
		t.Skip("set DEEPAGENT_TEST_MYSQL_DSN and DEEPAGENT_TEST_REDIS_ADDR for storage tests")
	}
	ctx := context.Background()
	client, err := daldb.NewSQL(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := client.DB(ctx, true).DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	err = daldb.MigrateMailbox(ctx, client.DB(ctx, true))
	if err != nil {
		t.Fatal(err)
	}
	counter, err := dalcache.NewRedis(dalcache.RedisConfig{Addr: addr})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := New(client, counter)
	if err != nil {
		t.Fatal(err)
	}
	id, err := dalcache.GenerateID(ctx, counter)
	if err != nil {
		t.Fatal(err)
	}
	thread := &agentmodel.ThreadRecord{ThreadID: id, Status: agentmodel.ThreadStatusOpen, LeaseToken: uuid.NewString()}
	err = manager.threads.Create(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Minute)
	thread.LeaseUntil = &until
	_, err = manager.threads.Update(ctx, &agentmodel.ThreadFilter{IDs: []int64{id}}, map[string]any{"lease_until": until})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.DB(ctx, true).Where("thread_id = ?", id).Delete(&agentmodel.MailboxMessage{})
		client.DB(ctx, true).Where("thread_id = ?", id).Delete(&agentmodel.ThreadRecord{})
		client.DB(ctx, true).Where("thread_id = ?", id).Delete(&agentmodel.RunRecord{})
	})
	return manager, thread
}

func TestManager_ReleaseKeepsRunOutcomeSeparate(t *testing.T) {
	for _, status := range []string{"finished", "blocked", "interrupted", "failed"} {
		t.Run(status, func(t *testing.T) {
			manager, thread := releaseTestManager(t)
			ctx := context.Background()
			payload := map[string]string{"status": status}
			if status == "blocked" {
				payload["checkpoint_id"], payload["interrupt_id"] = "checkpoint", "interrupt"
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			err = manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "run", []agentmodel.OutputFrame{{EventType: "run_status", Payload: raw}})
			if err != nil {
				t.Fatal(err)
			}
			released, err := manager.ReleaseThread(ctx, thread.ThreadID, thread.LeaseToken)
			if err != nil {
				t.Fatal(err)
			}
			if released.Status != agentmodel.ThreadStatusOpen || released.LeaseToken != "" || released.LastRunID != "run" || released.LastRun.Status != status {
				t.Fatalf("released=%+v outcome=%+v", released, released.LastRun)
			}

		})
	}
}

func TestManager_ReleaseIgnoresOutcomeFromPreviousLease(t *testing.T) {
	manager, thread := releaseTestManager(t)
	ctx := context.Background()
	err := manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "run", []agentmodel.OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"blocked","checkpoint_id":"checkpoint","interrupt_id":"interrupt"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.ReleaseThread(ctx, thread.ThreadID, thread.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Resume(ctx, thread.ThreadID, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Submit(ctx, agentmodel.SubmitRequest{ThreadID: thread.ThreadID, Input: &agentmodel.InputMessage{MessageType: "input", Payload: []byte(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := manager.Acquire(ctx, agentmodel.AcquireRequest{})
	if err != nil || acquired.Lease == nil {
		t.Fatalf("claim=%+v err=%v", acquired, err)
	}
	err = manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "run", []agentmodel.OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"finished"}`)}})
	if !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("old writer error=%v", err)
	}
	released, err := manager.ReleaseThread(ctx, thread.ThreadID, acquired.Lease.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	if released.Status != agentmodel.ThreadStatusOpen {
		t.Fatalf("old blocked outcome affected new lease: %+v", released)
	}
}

func TestManager_RunOutcomeRejectsStaleRunAndMissingCheckpoint(t *testing.T) {
	manager, thread := releaseTestManager(t)
	ctx := context.Background()
	err := manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "new-run", []agentmodel.OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"started"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range []agentmodel.OutputFrame{
		{RunID: "old-run", EventType: "run_status", Payload: []byte(`{"status":"finished"}`)},
		{RunID: "new-run", EventType: "run_status", Payload: []byte(`{"status":"blocked"}`)},
	} {
		err = manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, frame.RunID, []agentmodel.OutputFrame{frame})
		if err == nil {
			t.Fatalf("accepted invalid terminal frame: %+v", frame)
		}
	}
	rows, err := manager.threads.Get(ctx, &agentmodel.ThreadFilter{IDs: []int64{thread.ThreadID}, Primary: true})
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].LastRunID != "new-run" || rows[0].LastRun.Status != "started" {
		t.Fatalf("invalid event changed outcome: %+v", rows[0])
	}
}

func TestManager_ReleasePreservesClosingAndRejectsLostLease(t *testing.T) {
	manager, thread := releaseTestManager(t)
	ctx := context.Background()
	_, err := manager.ReleaseThread(ctx, thread.ThreadID, "wrong-token")
	if !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("wrong token error=%v", err)
	}
	_, err = manager.threads.Update(ctx, &agentmodel.ThreadFilter{IDs: []int64{thread.ThreadID}}, map[string]any{"status": agentmodel.ThreadStatusClosing})
	if err != nil {
		t.Fatal(err)
	}
	err = manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "run", []agentmodel.OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"blocked","checkpoint_id":"checkpoint","interrupt_id":"interrupt"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	released, err := manager.ReleaseThread(ctx, thread.ThreadID, thread.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	if released.Status != agentmodel.ThreadStatusClosing || released.LeaseToken != "" || released.LeaseUntil != nil {
		t.Fatalf("closing lost: %+v", released)
	}
}

func TestManager_RunOutcomeIncludesCompactionAndStartupFailure(t *testing.T) {
	manager, thread := releaseTestManager(t)
	ctx := context.Background()
	steps := []struct{ run, status, want string }{
		{"first", "started", "started"},
		{"first", "finished", "finished"},
		{"compact", "compact_started", "started"},
		{"compact", "context_compacted", "started"},
		{"compact", "finished", "finished"},
		{"startup-failure", "failed", "failed"},
		{"second", "started", "started"},
		{"second", "compact_started", "started"},
		{"second", "context_compacted", "started"},
		{"second", "finished", "finished"},
	}
	for _, step := range steps {
		raw, err := json.Marshal(map[string]string{"status": step.status})
		if err != nil {
			t.Fatal(err)
		}
		err = manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, step.run, []agentmodel.OutputFrame{{EventType: "run_status", Payload: raw}})
		if err != nil {
			t.Fatalf("step=%+v error=%v", step, err)
		}
		rows, err := manager.threads.Get(ctx, &agentmodel.ThreadFilter{IDs: []int64{thread.ThreadID}, Primary: true})
		if err != nil {
			t.Fatal(err)
		}
		if rows[0].LastRunID != step.run || rows[0].LastRun.Status != step.want {
			t.Fatalf("step=%+v outcome=%+v", step, rows[0])
		}
	}
}

func TestSessionSequenceResumesFromExistingCounter(t *testing.T) {
	addr := os.Getenv("DEEPAGENT_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set DEEPAGENT_TEST_REDIS_ADDR for session replay validation")
	}
	redis, err := dalcache.NewRedis(dalcache.RedisConfig{Addr: addr})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sessionID := "sequence-test-" + uuid.NewString()
	key := sessionEventKey(sessionID)
	t.Cleanup(func() {
		_, _ = redis.Del(context.Background(), key, key+":seq", key+":16", key+":17")
	})
	_, err = redis.IncrBy(ctx, key+":seq", 15)
	if err != nil {
		t.Fatal(err)
	}
	stream := &StreamStreamOut{redis: redis}
	err = stream.FanoutEventRecords(ctx, sessionID, []agentmodel.OutputFrame{
		{EventType: "assistant_message", Payload: []byte(`{"text":"first"}`)},
		{EventType: "assistant_message", Payload: []byte(`{"text":"second"}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	sequence, err := redis.GetCounter(ctx, key+":seq")
	if err != nil || sequence != 17 {
		t.Fatalf("session counter changed namespace or reset: sequence=%d err=%v", sequence, err)
	}
	manager := &Manager{stream: stream, subscribeSessionMaxIdle: time.Second}
	subscription, err := manager.SubscribeSession(ctx, sessionID, "16")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	select {
	case frame, open := <-subscription.Events:
		if !open || frame.QueueID != "17" || string(frame.Payload) != `{"text":"second"}` {
			t.Fatalf("reconnect did not continue after existing cursor: %+v", frame)
		}
	case <-ctx.Done():
		t.Fatal("session replay timed out")
	}
}

func TestManager_OutputRoutingPreservesDurableKeys(t *testing.T) {
	for _, scenario := range []struct {
		name, kind, payload, messageType, key, sender string
		originalID                                    int64
	}{
		{"assistant", "assistant_message", `{"llm_response_id":"response"}`, "assistant", "response", agentmodel.MailboxSenderTypeAgent, 77},
		{"assistant fallback", "assistant_message", `{}`, "assistant", "77", agentmodel.MailboxSenderTypeAgent, 77},
		{"tool", "tool_call", `{"tool_call_id":"call"}`, "tool", "call", agentmodel.MailboxSenderTypeAgent, 77},
		{"approval", "input_required", `{"kind":"approval","interrupt_id":"question"}`, "approval", "question", agentmodel.MailboxSenderTypeSystem, 77},
		{"question", "input_required", `{"kind":"plan_input","interrupt_id":"question"}`, "question", "question", agentmodel.MailboxSenderTypeSystem, 77},
		{"interrupt", "input_required", `{"kind":"custom","interrupt_id":"question"}`, "interrupt", "question", agentmodel.MailboxSenderTypeSystem, 77},
		{"latest plan", "plan_updated", `{}`, "plan", "latest", agentmodel.MailboxSenderTypeSystem, 77},
		{"error event ID", "error", `{"message":"failed"}`, "error", "77", agentmodel.MailboxSenderTypeSystem, 77},
		{"error hash", "error", `{"message":"failed"}`, "error", "", agentmodel.MailboxSenderTypeSystem, 0},
		{"empty tool delta", "tool_call", `{"tool_call_id":"call","output_delta":""}`, "", "", "", 77},
		{"tool delta", "tool_call", `{"tool_call_id":"call","output_delta":"partial"}`, "", "", "", 77},
		{"assistant delta", "assistant_delta", `{}`, "", "", "", 77},
		{"activity", "agent_activity", `{}`, "", "", "", 77},
		{"input consumed", "input_consumed", `{}`, "", "", "", 77},
		{"unknown", "extension_event", `{}`, "", "", "", 77},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			manager, thread := releaseTestManager(t)
			ctx := context.Background()
			frame := agentmodel.OutputFrame{EventID: scenario.originalID, EventType: scenario.kind, Payload: []byte(scenario.payload)}
			// 重投原始输出仍只更新同一条记录，重新分配的 EventID 不能改变去重键。
			for range 2 {
				err := manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "run", []agentmodel.OutputFrame{frame})
				if err != nil {
					t.Fatal(err)
				}
			}
			rows, err := manager.messages.Get(ctx, &agentmodel.MailboxMessageFilter{ThreadIDs: []int64{thread.ThreadID}, Primary: true})
			if err != nil {
				t.Fatal(err)
			}
			if scenario.messageType == "" {
				if len(rows) != 0 {
					t.Fatalf("live event became durable: %+v", rows)
				}
				return
			}
			key := scenario.key
			if key == "" {
				key = fmt.Sprintf("%x", sha256.Sum256(frame.Payload))
			}
			key = "run:" + scenario.messageType + ":" + key
			if len(rows) != 1 || rows[0].OutputKey == nil || *rows[0].OutputKey != key || rows[0].MessageType != scenario.messageType || rows[0].Sender.Type != scenario.sender {
				t.Fatalf("key=%s rows=%+v", key, rows)
			}
		})
	}
}

func TestManager_InvalidLivePayloadRollsBackWholeBatch(t *testing.T) {
	for _, kind := range []string{"extension_event", "assistant_delta", "tool_call"} {
		t.Run(kind, func(t *testing.T) {
			manager, thread := releaseTestManager(t)
			ctx := context.Background()
			err := manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "run", []agentmodel.OutputFrame{
				{EventType: "assistant_message", Payload: []byte(`{"llm_response_id":"response"}`)},
				{EventType: kind, Payload: []byte(`{"broken":`)},
			})
			if err == nil {
				t.Fatal("malformed live payload accepted")
			}
			rows, err := manager.messages.Get(ctx, &agentmodel.MailboxMessageFilter{ThreadIDs: []int64{thread.ThreadID}, Primary: true})
			if err != nil || len(rows) != 0 {
				t.Fatalf("batch not rolled back: rows=%+v err=%v", rows, err)
			}
		})
	}
}
