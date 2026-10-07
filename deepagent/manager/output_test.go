package manager

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	dalcache "eino-cli/deepagent/dal/cache"
	daldb "eino-cli/deepagent/dal/db"
	dalmodel "eino-cli/deepagent/dal/model"
	eventpkg "eino-cli/deepagent/protocol/event"
	"github.com/google/uuid"
)

func TestToolCallOutputRoutingFromPublicPayload(t *testing.T) {
	delta, result := "partial result", "completed result"
	cases := []struct {
		name    string
		payload eventpkg.ToolCallEventPayload
		action  outputEventAction
	}{
		{"streaming", eventpkg.ToolCallEventPayload{ToolCallID: "call-1", OutputDelta: &delta}, outputActionLiveOnly},
		{"completed", eventpkg.ToolCallEventPayload{ToolCallID: "call-1", ResultJSON: &result, Status: eventpkg.ToolCallStatusFinished}, outputActionSaveMessage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := parseOutputPayload(raw)
			if err != nil {
				t.Fatal(err)
			}
			rule := outputEventRuleFor(eventpkg.EventTypeToolCall.String(), payload)
			if rule.action != tc.action || rule.messageType != "tool" {
				t.Fatalf("rule = %+v, want action %d and tool message type", rule, tc.action)
			}
			if tc.payload.OutputDelta != nil && (payload.OutputDelta == nil || *payload.OutputDelta != delta) {
				t.Fatalf("output delta lost in %s", raw)
			}
		})
	}
}

// These tests use an isolated database selected explicitly by the caller.
func releaseTestManager(t *testing.T) (*Manager, *dalmodel.Thread) {
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
	thread := &dalmodel.Thread{ThreadID: id, Status: dalmodel.ThreadStatusOpen, LeaseToken: uuid.NewString()}
	err = manager.threads.Create(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Minute)
	thread.LeaseUntil = &until
	_, err = manager.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{id}}, map[string]any{"lease_until": until})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.DB(ctx, true).Where("thread_id = ?", id).Delete(&dalmodel.Message{})
		client.DB(ctx, true).Where("thread_id = ?", id).Delete(&dalmodel.Thread{})
		client.DB(ctx, true).Where("thread_id = ?", id).Delete(&dalmodel.RunRecord{})
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
			err = manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "run", []OutputFrame{{EventType: "run_status", Payload: raw}})
			if err != nil {
				t.Fatal(err)
			}
			released, err := manager.ReleaseThread(ctx, thread.ThreadID, thread.LeaseToken)
			if err != nil {
				t.Fatal(err)
			}
			if released.Status != dalmodel.ThreadStatusOpen || released.LeaseToken != "" || released.LastRunID != "run" || released.LastRun.Status != status {
				t.Fatalf("released=%+v outcome=%+v", released, released.LastRun)
			}

		})
	}
}

func TestManager_ReleaseIgnoresOutcomeFromPreviousLease(t *testing.T) {
	manager, thread := releaseTestManager(t)
	ctx := context.Background()
	err := manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "run", []OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"blocked","checkpoint_id":"checkpoint","interrupt_id":"interrupt"}`)}})
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
	_, err = manager.Submit(ctx, SubmitRequest{ThreadID: thread.ThreadID, Input: &InputMessage{MessageType: "input", Payload: []byte(`{}`)}})
	if err != nil {
		t.Fatal(err)
	}
	acquired, err := manager.Acquire(ctx, AcquireRequest{})
	if err != nil || acquired.Lease == nil {
		t.Fatalf("claim=%+v err=%v", acquired, err)
	}
	err = manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "run", []OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"finished"}`)}})
	if !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("old writer error=%v", err)
	}
	released, err := manager.ReleaseThread(ctx, thread.ThreadID, acquired.Lease.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	if released.Status != dalmodel.ThreadStatusOpen {
		t.Fatalf("old blocked outcome affected new lease: %+v", released)
	}
}

func TestManager_RunOutcomeRejectsStaleRunAndMissingCheckpoint(t *testing.T) {
	manager, thread := releaseTestManager(t)
	ctx := context.Background()
	err := manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "new-run", []OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"started"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range []OutputFrame{
		{RunID: "old-run", EventType: "run_status", Payload: []byte(`{"status":"finished"}`)},
		{RunID: "new-run", EventType: "run_status", Payload: []byte(`{"status":"blocked"}`)},
	} {
		err = manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, frame.RunID, []OutputFrame{frame})
		if err == nil {
			t.Fatalf("accepted invalid terminal frame: %+v", frame)
		}
	}
	rows, err := manager.threads.Get(ctx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}, Primary: true})
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
	_, err = manager.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}}, map[string]any{"status": dalmodel.ThreadStatusClosing})
	if err != nil {
		t.Fatal(err)
	}
	err = manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "run", []OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"blocked","checkpoint_id":"checkpoint","interrupt_id":"interrupt"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	released, err := manager.ReleaseThread(ctx, thread.ThreadID, thread.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	if released.Status != dalmodel.ThreadStatusClosing || released.LeaseToken != "" || released.LeaseUntil != nil {
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
		err = manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, step.run, []OutputFrame{{EventType: "run_status", Payload: raw}})
		if err != nil {
			t.Fatalf("step=%+v error=%v", step, err)
		}
		rows, err := manager.threads.Get(ctx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}, Primary: true})
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
	err = stream.FanoutEventRecords(ctx, sessionID, []OutputFrame{
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
