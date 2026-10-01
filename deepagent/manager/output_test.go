package manager

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	dalcache "eino-cli/deepagent/dal/cache"
	daldb "eino-cli/deepagent/dal/db"
	dalmodel "eino-cli/deepagent/dal/model"
	eventpkg "eino-cli/deepagent/protocol/event"
	"github.com/google/uuid"
	redispkg "github.com/redis/go-redis/v9"
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
	id, err := IDNextSharedID(ctx, counter)
	if err != nil {
		t.Fatal(err)
	}
	thread := &dalmodel.Thread{ThreadID: id, Status: dalmodel.ThreadStatusRunning, LeaseToken: uuid.NewString()}
	err = manager.threads.Create(ctx, thread)
	if err != nil {
		t.Fatal(err)
	}
	// Create omits ready_until; install the lease explicitly for this fixture.
	thread.ReadyUntil = time.Now().Add(time.Minute)
	_, err = manager.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{id}}, map[string]any{"ready_until": thread.ReadyUntil})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.DB(ctx, true).Where("thread_id = ?", id).Delete(&dalmodel.Message{})
		client.DB(ctx, true).Where("thread_id = ?", id).Delete(&dalmodel.Thread{})
		_, _ = counter.Del(ctx, RedisPendingInputKey(id), RedisAcceptedInputKey(id))
	})
	return manager, thread
}

func TestManager_ReleaseUsesSavedOutcome(t *testing.T) {
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
			want := dalmodel.ThreadStatusIdle
			if status == "blocked" {
				want = dalmodel.ThreadStatusBlocked
			}
			if released.Status != want || released.LeaseToken != "" || released.RunID != "run" || released.RunStatus != status {
				t.Fatalf("released=%+v want status=%q outcome=%q", released, want, status)
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
	if released.Status != dalmodel.ThreadStatusReady {
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
	if rows[0].RunID != "new-run" || rows[0].RunStatus != "started" {
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
	if released.Status != dalmodel.ThreadStatusClosing || released.LeaseToken != "" || released.ReadyUntil.IsZero() {
		t.Fatalf("closing lost: %+v", released)
	}
}

type queueGate struct {
	dalcache.RedisClient
	operation string
	entered   chan struct{}
	proceed   chan struct{}
	once      sync.Once
}

func (g *queueGate) wait(ctx context.Context, operation string) error {
	if operation != g.operation {
		return nil
	}
	g.once.Do(func() { close(g.entered) })
	select {
	case <-g.proceed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (g *queueGate) ZRange(ctx context.Context, key string, start, stop int64) ([]string, error) {
	err := g.wait(ctx, "read")
	if err != nil {
		return nil, err
	}
	return g.RedisClient.ZRange(ctx, key, start, stop)
}
func (g *queueGate) ZAdd(ctx context.Context, key string, members []redispkg.Z) (int64, error) {
	err := g.wait(ctx, "enqueue")
	if err != nil {
		return 0, err
	}
	return g.RedisClient.ZAdd(ctx, key, members)
}

func TestManager_SubmitAndReleaseShareThreadLock(t *testing.T) {
	for _, first := range []string{"release", "submit"} {
		t.Run(first, func(t *testing.T) {
			manager, thread := releaseTestManager(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			operation := "read"
			if first == "submit" {
				operation = "enqueue"
			}
			gate := &queueGate{RedisClient: manager.redis, operation: operation, entered: make(chan struct{}), proceed: make(chan struct{})}
			manager.redis = gate
			submit := func() error {
				_, err := manager.Submit(ctx, SubmitRequest{ThreadID: thread.ThreadID, Input: &InputMessage{MessageType: "input", Payload: []byte(`{}`)}})
				return err
			}
			release := func() error {
				_, err := manager.ReleaseThread(ctx, thread.ThreadID, thread.LeaseToken)
				return err
			}
			firstCall, secondCall := release, submit
			if first == "submit" {
				firstCall, secondCall = submit, release
			}
			firstDone, secondDone := make(chan error, 1), make(chan error, 1)
			go func() { firstDone <- firstCall() }()
			select {
			case <-gate.entered:
			case <-ctx.Done():
				t.Fatal("first operation did not reach queue")
			}
			go func() { secondDone <- secondCall() }()
			select {
			case err := <-secondDone:
				close(gate.proceed)
				t.Fatalf("second operation bypassed Thread lock: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			close(gate.proceed)
			for _, done := range []chan error{firstDone, secondDone} {
				err := <-done
				if err != nil {
					t.Fatal(err)
				}
			}
			rows, err := manager.threads.Get(ctx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}, Primary: true})
			if err != nil {
				t.Fatal(err)
			}
			if rows[0].Status != dalmodel.ThreadStatusReady || rows[0].LeaseToken != "" {
				t.Fatalf("input not scheduled: %+v", rows[0])
			}
			pending, err := manager.readQueuedMessages(ctx, thread.ThreadID, false)
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending=%v error=%v", pending, err)
			}
		})
	}
}

func TestManager_RunOutcomeIncludesCompactionAndStartupFailure(t *testing.T) {
	manager, thread := releaseTestManager(t)
	ctx := context.Background()
	steps := []struct{ run, status, want string }{
		{"first", "started", "started"},
		{"first", "finished", "finished"},
		{"compact", "compact_started", "compact_started"},
		{"compact", "context_compacted", "compact_started"},
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
		if rows[0].RunID != step.run || rows[0].RunStatus != step.want {
			t.Fatalf("step=%+v outcome=%+v", step, rows[0])
		}
	}
}

type uncertainEnqueue struct{ dalcache.RedisClient }

func (r uncertainEnqueue) ZAdd(ctx context.Context, key string, values []redispkg.Z) (int64, error) {
	count, err := r.RedisClient.ZAdd(ctx, key, values)
	if err != nil {
		return count, err
	}
	return count, errors.New("enqueue response lost")
}
func TestManager_SubmitRollsBackUncertainEnqueue(t *testing.T) {
	manager, thread := releaseTestManager(t)
	ctx := context.Background()
	manager.redis = uncertainEnqueue{manager.redis}
	_, err := manager.Submit(ctx, SubmitRequest{ThreadID: thread.ThreadID, Input: &InputMessage{MessageType: "input", Payload: []byte(`{}`)}})
	if err == nil {
		t.Fatal("enqueue error was ignored")
	}
	pending, err := manager.readQueuedMessages(ctx, thread.ThreadID, false)
	if err != nil || len(pending) != 0 {
		t.Fatalf("rollback left queue entries: %v %v", pending, err)
	}
	messages, err := manager.messages.Get(ctx, &dalmodel.MessageFilter{ThreadIDs: []int64{thread.ThreadID}, Primary: true})
	if err != nil || len(messages) != 0 {
		t.Fatalf("rollback left messages: %v %v", messages, err)
	}
}
