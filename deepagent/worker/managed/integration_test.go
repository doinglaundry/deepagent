package managed

import (
	"context"
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/manager/compat"
	"eino-cli/deepagent/protocol"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCompetingWorkersExecuteOneClaimWithMemoryManager(t *testing.T) {
	m := manager.NewMemory("competition")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	input := protocol.Input{Kind: protocol.InputUser, Text: "one execution"}
	thread, err := m.CreateThread(ctx, api.CreateThreadRequest{WorkDir: t.TempDir(), Input: &input})
	if err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	factory := func(context.Context, api.Claim) (Runtime, error) {
		count.Add(1)
		r := &boundaryRuntime{events: make(chan protocol.Event, 8)}
		r.post = func(in protocol.Input) {
			r.events <- protocol.Event{Kind: protocol.EventText, RunID: "run", Text: "answer", MessageIDs: []string{in.ID}}
			r.events <- protocol.Event{Kind: protocol.EventRunCompleted, RunID: "run", MessageIDs: []string{in.ID}}
		}
		return r, nil
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		w, err := New(m, factory, Config{PollInterval: time.Millisecond, PermitTTL: time.Second, ShutdownGrace: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func() { defer wg.Done(); _ = w.Run(ctx) }()
	}
	defer wg.Wait()
	defer cancel()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		events, err := m.ListEvents(ctx, api.EventFilter{ThreadID: thread.ID})
		if err != nil {
			t.Fatal(err)
		}
		state, err := m.GetThread(ctx, thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 2 && state.State == api.Idle {
			if count.Load() != 1 {
				t.Fatalf("executed %d times", count.Load())
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("work did not finish")
}
func TestShutdownRenewsDuringGraceThenDrainsCancellation(t *testing.T) {
	m := testManager(protocol.Input{ID: "input", Kind: protocol.InputUser, Text: "long running"})
	started := make(chan struct{})
	var once sync.Once
	r := &boundaryRuntime{events: make(chan protocol.Event, 4), post: func(protocol.Input) { close(started) }}
	r.cancel = func() {
		once.Do(func() {
			r.events <- protocol.Event{Kind: protocol.EventRunCancelled, RunID: "run", MessageIDs: []string{"input"}}
		})
	}
	cancel, _ := runWorker(t, m, r)
	await(t, started)
	cancel()
	await(t, m.done)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.renews == 0 {
		t.Fatal("permit was not renewed during graceful shutdown")
	}
	if len(m.events) != 1 || m.events[0].Kind != protocol.EventRunCancelled {
		t.Fatalf("final events not drained: %v", m.events)
	}
}

func TestFailedResumeAssemblyRetainsCheckpointForRetry(t *testing.T) {
	ctx := context.Background()
	m := manager.NewMemory("failed-resume")
	input := protocol.Input{Kind: protocol.InputUser, Text: "change file"}
	thread, err := m.CreateThread(ctx, api.CreateThreadRequest{WorkDir: t.TempDir(), Input: &input})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := m.ClaimThread(ctx, thread.ID, "first", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err = m.ConfirmInputDelivery(ctx, claim.Permit, claim.Inputs[0].ID); err != nil {
		t.Fatal(err)
	}
	block := &protocol.Block{RunID: "run", CheckpointID: "checkpoint", InterruptID: "approval", Kind: "approval"}
	if _, err = m.PublishEvent(ctx, claim.Permit, protocol.Event{Kind: protocol.EventBlocked, RunID: "run", MessageIDs: []string{claim.Inputs[0].ID}, Block: block}); err != nil {
		t.Fatal(err)
	}
	if err = m.ReleaseThread(ctx, claim.Permit, api.Release{Block: block}); err != nil {
		t.Fatal(err)
	}
	resume, err := m.ResumeFromBlock(ctx, thread.ID, protocol.Input{Kind: protocol.InputResume, Resume: &protocol.Resume{RunID: "run", CheckpointID: "checkpoint", InterruptID: "approval", Approved: true}})
	if err != nil {
		t.Fatal(err)
	}
	claim, err = m.ClaimThread(ctx, thread.ID, "second", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	w, err := New(m, func(context.Context, api.Claim) (Runtime, error) { return nil, errors.New("missing shared mount") }, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = w.execute(ctx, claim); err == nil {
		t.Fatal("expected setup error")
	}
	state, err := m.GetThread(ctx, thread.ID)
	if err != nil || state.State != api.Blocked || state.Block.CheckpointID != "checkpoint" {
		t.Fatalf("state %+v err %v", state, err)
	}
	events, err := m.ListEvents(ctx, api.EventFilter{ThreadID: thread.ID})
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Kind != protocol.EventRunFailed || last.RunID != "run" || len(last.MessageIDs) != 1 || last.MessageIDs[0] != resume.ID {
		t.Fatalf("failure %+v", last)
	}
}
