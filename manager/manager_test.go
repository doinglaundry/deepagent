package manager

import (
	"context"
	"eino-cli/manager/api"
	"eino-cli/protocol"
	"errors"
	"sync"
	"testing"
	"time"
)

var ctx = context.Background()

func setup(t *testing.T) (*Memory, api.Thread, protocol.Input) {
	t.Helper()
	m := NewMemory("test")
	in := protocol.Input{Kind: protocol.InputUser, Text: "hello"}
	th, err := m.CreateThread(ctx, api.CreateThreadRequest{WorkDir: t.TempDir(), Input: &in})
	if err != nil {
		t.Fatal(err)
	}
	c, err := m.ClaimThread(ctx, th.ID, "setup", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	in = c.Inputs[0]
	if err = m.ReleaseThread(ctx, c.Permit, api.Release{}); err != nil {
		t.Fatal(err)
	}
	return m, th, in
}
func TestClaimCompetitionAndFencing(t *testing.T) {
	m, th, _ := setup(t)
	var wg sync.WaitGroup
	wins := make(chan api.Claim, 20)
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := m.ClaimThread(ctx, th.ID, "worker", time.Minute)
			if e == nil {
				wins <- c
			}
		}()
	}
	wg.Wait()
	close(wins)
	if len(wins) != 1 {
		t.Fatalf("claim winners = %d", len(wins))
	}
	c := <-wins
	bad := c.Permit
	bad.Token = "stale"
	if _, e := m.RenewThreadPermit(ctx, bad, time.Minute); !errors.Is(e, api.ErrPermitLost) {
		t.Fatal(e)
	}
	if _, e := m.PublishEvent(ctx, bad, protocol.Event{Kind: protocol.EventRunCompleted}); !errors.Is(e, api.ErrPermitLost) {
		t.Fatal(e)
	}
}
func TestAcceptedIncompleteRedeliveredAfterExpiry(t *testing.T) {
	m, th, in := setup(t)
	c, e := m.ClaimThread(ctx, th.ID, "old", 50*time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.ConfirmInputDelivery(ctx, c.Permit, in.ID); e != nil {
		t.Fatal(e)
	}
	time.Sleep(60 * time.Millisecond)
	next, e := m.ClaimThread(ctx, th.ID, "new", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if len(next.Inputs) != 1 || next.Inputs[0].ID != in.ID {
		t.Fatalf("lost accepted input: %+v", next.Inputs)
	}
	if e = m.ConfirmInputDelivery(ctx, c.Permit, in.ID); !errors.Is(e, api.ErrPermitLost) {
		t.Fatal(e)
	}
}
func TestTerminalEventSuppressesRecovery(t *testing.T) {
	m, th, in := setup(t)
	c, _ := m.ClaimThread(ctx, th.ID, "old", 20*time.Millisecond)
	if e := m.ConfirmInputDelivery(ctx, c.Permit, in.ID); e != nil {
		t.Fatal(e)
	}
	_, e := m.PublishEvent(ctx, c.Permit, protocol.Event{Kind: protocol.EventRunCompleted, RunID: "run", MessageIDs: []string{in.ID}})
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(25 * time.Millisecond)
	next, e := m.ClaimThread(ctx, th.ID, "new", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if len(next.Inputs) != 0 {
		t.Fatal(next.Inputs)
	}
}
func TestBlockedResumeCorrelation(t *testing.T) {
	m, th, in := setup(t)
	c, _ := m.ClaimThread(ctx, th.ID, "w", time.Minute)
	m.ConfirmInputDelivery(ctx, c.Permit, in.ID)
	b := &protocol.Block{RunID: "run", CheckpointID: "cp", InterruptID: "int"}
	if e := m.ReleaseThread(ctx, c.Permit, api.Release{Block: b}); e != nil {
		t.Fatal(e)
	}
	r := protocol.Input{Kind: protocol.InputResume, Resume: &protocol.Resume{RunID: "wrong", CheckpointID: "cp", InterruptID: "int"}}
	if _, e := m.ResumeFromBlock(ctx, th.ID, r); !errors.Is(e, api.ErrConflict) {
		t.Fatal(e)
	}
	r.Resume.RunID = "run"
	got, e := m.ResumeFromBlock(ctx, th.ID, r)
	if e != nil {
		t.Fatal(e)
	}
	next, e := m.ClaimThread(ctx, th.ID, "w2", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if next.Inputs[0].ID != got.ID {
		t.Fatal(next.Inputs)
	}
}
func TestCancelCutoffAndClose(t *testing.T) {
	m, th, first := setup(t)
	second, e := m.SubmitInput(ctx, th.ID, protocol.Input{Kind: protocol.InputUser, Text: "second"})
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Cancel(ctx, th.ID, first.ID); e != nil {
		t.Fatal(e)
	}
	c, e := m.ClaimThread(ctx, th.ID, "w", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if len(c.Inputs) != 2 || c.Inputs[0].Kind != protocol.InputCancel || c.Inputs[1].ID != second.ID {
		t.Fatal(c.Inputs)
	}
	if e = m.RequestThreadClose(ctx, th.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = m.SubmitInput(ctx, th.ID, protocol.Input{Kind: protocol.InputUser, Text: "late"}); !errors.Is(e, api.ErrClosed) {
		t.Fatal(e)
	}
	if e = m.ConfirmThreadClosed(ctx, c.Permit); e != nil {
		t.Fatal(e)
	}
	got, _ := m.GetThread(ctx, th.ID)
	if got.State != api.Closed {
		t.Fatal(got.State)
	}
}
func TestDurableEventsHistoryAndNamespace(t *testing.T) {
	m, th, in := setup(t)
	c, _ := m.ClaimThread(ctx, th.ID, "w", time.Minute)
	sub, e := m.SubscribeSession(ctx, th.SessionID)
	if e != nil {
		t.Fatal(e)
	}
	defer sub.Close()
	event, e := m.PublishEvent(ctx, c.Permit, protocol.Event{Kind: protocol.EventRunStarted, RunID: "run", MessageIDs: []string{in.ID}})
	if e != nil {
		t.Fatal(e)
	}
	select {
	case got := <-sub.Events:
		persisted, e := m.ListEvents(ctx, api.EventFilter{After: 0, SessionID: th.SessionID})
		if e != nil || len(persisted) != 1 || persisted[0].ID != got.ID {
			t.Fatalf("published before persistence: %v %v", persisted, e)
		}
	case <-time.After(time.Second):
		t.Fatal("missing event")
	}
	again, e := m.PublishEvent(ctx, c.Permit, event)
	if e != nil || again.Sequence != event.Sequence {
		t.Fatalf("idempotency %v %v", again, e)
	}
	h, e := m.SaveHistory(ctx, c.Permit, api.History{Messages: []byte("[]")})
	if e != nil || h.Version != 1 {
		t.Fatal(h, e)
	}
	if _, e = m.SaveHistory(ctx, c.Permit, api.History{}); !errors.Is(e, api.ErrConflict) {
		t.Fatal(e)
	}
	other := NewMemory("other")
	if _, e = other.GetThread(ctx, th.ID); !errors.Is(e, api.ErrNotFound) {
		t.Fatal(e)
	}
	if e = m.PutCheckpoint(ctx, "cp", []byte("data")); e != nil {
		t.Fatal(e)
	}
	if _, e = other.GetCheckpoint(ctx, "cp"); !errors.Is(e, api.ErrNotFound) {
		t.Fatal(e)
	}
}

func TestAcknowledgedCancelDoesNotLoop(t *testing.T) {
	m, th, _ := setup(t)
	if e := m.Cancel(ctx, th.ID, ""); e != nil {
		t.Fatal(e)
	}
	c, e := m.ClaimThread(ctx, th.ID, "w", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.ConfirmInputDelivery(ctx, c.Permit, c.Inputs[0].ID); e != nil {
		t.Fatal(e)
	}
	if e = m.ReleaseThread(ctx, c.Permit, api.Release{}); e != nil {
		t.Fatal(e)
	}
	got, _ := m.GetThread(ctx, th.ID)
	if got.State != api.Idle {
		t.Fatalf("cancel replayed indefinitely: %s", got.State)
	}
}
func TestBlockedEventSurvivesCrashBeforeRelease(t *testing.T) {
	m, th, in := setup(t)
	c, _ := m.ClaimThread(ctx, th.ID, "old", 20*time.Millisecond)
	m.ConfirmInputDelivery(ctx, c.Permit, in.ID)
	b := &protocol.Block{RunID: "run", CheckpointID: "cp", InterruptID: "int"}
	_, e := m.PublishEvent(ctx, c.Permit, protocol.Event{Kind: protocol.EventBlocked, RunID: "run", MessageIDs: []string{in.ID}, Block: b})
	if e != nil {
		t.Fatal(e)
	}
	time.Sleep(25 * time.Millisecond)
	_, e = m.ClaimThread(ctx, th.ID, "new", time.Minute)
	if !errors.Is(e, api.ErrConflict) {
		t.Fatalf("expected recovered block: %v", e)
	}
	got, _ := m.GetThread(ctx, th.ID)
	if got.State != api.Blocked || got.Block == nil || got.Block.CheckpointID != "cp" {
		t.Fatalf("lost durable interrupt: %+v", got)
	}
}
func TestEventIDCannotChangeMeaning(t *testing.T) {
	m, th, in := setup(t)
	c, _ := m.ClaimThread(ctx, th.ID, "w", time.Minute)
	e, err := m.PublishEvent(ctx, c.Permit, protocol.Event{Kind: protocol.EventRunStarted, RunID: "run", MessageIDs: []string{in.ID}})
	if err != nil {
		t.Fatal(err)
	}
	e.Kind = protocol.EventRunCompleted
	if _, err = m.PublishEvent(ctx, c.Permit, e); !errors.Is(err, api.ErrConflict) {
		t.Fatalf("reused event ID changed meaning: %v", err)
	}
}

func TestCancelWinsOverLateBlockedRelease(t *testing.T) {
	m, th, in := setup(t)
	c, _ := m.ClaimThread(ctx, th.ID, "w", time.Minute)
	m.ConfirmInputDelivery(ctx, c.Permit, in.ID)
	if e := m.Cancel(ctx, th.ID, in.ID); e != nil {
		t.Fatal(e)
	}
	b := &protocol.Block{RunID: "run", CheckpointID: "cp", InterruptID: "int"}
	if e := m.ReleaseThread(ctx, c.Permit, api.Release{Block: b}); e != nil {
		t.Fatal(e)
	}
	got, _ := m.GetThread(ctx, th.ID)
	if got.State != api.Ready || got.Block != nil {
		t.Fatalf("cancel stranded behind block: %+v", got)
	}
}
func TestEveryWorkerMutationRejectsExpiredPermit(t *testing.T) {
	m, th, in := setup(t)
	c, _ := m.ClaimThread(ctx, th.ID, "w", time.Millisecond)
	time.Sleep(5 * time.Millisecond)
	checks := []func() error{func() error { _, e := m.ReadPendingInputs(ctx, c.Permit); return e }, func() error { return m.ConfirmInputDelivery(ctx, c.Permit, in.ID) }, func() error { return m.ReleaseThread(ctx, c.Permit, api.Release{}) }, func() error { return m.ConfirmThreadClosed(ctx, c.Permit) }, func() error { _, e := m.SaveHistory(ctx, c.Permit, api.History{}); return e }, func() error {
		_, e := m.PublishEvent(ctx, c.Permit, protocol.Event{Kind: protocol.EventTextDelta})
		return e
	}}
	for i, check := range checks {
		if e := check(); !errors.Is(e, api.ErrPermitLost) {
			t.Fatalf("mutation %d: %v", i, e)
		}
	}
}

func TestTransientEventsAndSubscriptionLifecycle(t *testing.T) {
	m, th, _ := setup(t)
	c, _ := m.ClaimThread(ctx, th.ID, "w", time.Minute)
	life, cancel := context.WithCancel(ctx)
	sub, e := m.SubscribeSession(life, th.SessionID)
	if e != nil {
		t.Fatal(e)
	}
	before, _ := m.store.load(ctx, th.ID)
	event, e := m.PublishEvent(ctx, c.Permit, protocol.Event{Kind: protocol.EventTextDelta, Text: "token"})
	after, _ := m.store.load(ctx, th.ID)
	if after.Revision != before.Revision {
		t.Fatal("transient output rewrote durable scheduling/queue state")
	}
	if e != nil || event.Sequence != 0 {
		t.Fatal(event, e)
	}
	select {
	case <-sub.Events:
	case <-time.After(time.Second):
		t.Fatal("missing transient event")
	}
	events, e := m.ListEvents(ctx, api.EventFilter{ThreadID: th.ID})
	if e != nil || len(events) != 0 {
		t.Fatal(events, e)
	}
	cancel()
	select {
	case _, open := <-sub.Events:
		if open {
			t.Fatal("subscription remained open")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription leaked")
	}
	sub.Close()
	if e = m.Close(); e != nil {
		t.Fatal(e)
	}
}

func TestAcknowledgedCancelWinsOverLateBlockedRelease(t *testing.T) {
	m, th, in := setup(t)
	c, _ := m.ClaimThread(ctx, th.ID, "w", time.Minute)
	m.ConfirmInputDelivery(ctx, c.Permit, in.ID)
	if e := m.Cancel(ctx, th.ID, in.ID); e != nil {
		t.Fatal(e)
	}
	controls, e := m.ReadPendingInputs(ctx, c.Permit)
	if e != nil || len(controls) != 1 {
		t.Fatal(controls, e)
	}
	if e = m.ConfirmInputDelivery(ctx, c.Permit, controls[0].ID); e != nil {
		t.Fatal(e)
	}
	b := &protocol.Block{RunID: "run", CheckpointID: "cp", InterruptID: "int"}
	if e = m.ReleaseThread(ctx, c.Permit, api.Release{Block: b}); e != nil {
		t.Fatal(e)
	}
	got, _ := m.GetThread(ctx, th.ID)
	if got.State != api.Idle || got.Block != nil {
		t.Fatalf("completed cancel reblocked: %+v", got)
	}
}

func TestPartialCancelPreservesLaterAcceptedInput(t *testing.T) {
	m, th, first := setup(t)
	second, e := m.SubmitInput(ctx, th.ID, protocol.Input{Kind: protocol.InputUser, Text: "after cutoff"})
	if e != nil {
		t.Fatal(e)
	}
	c, _ := m.ClaimThread(ctx, th.ID, "w", time.Minute)
	for _, in := range c.Inputs {
		if e = m.ConfirmInputDelivery(ctx, c.Permit, in.ID); e != nil {
			t.Fatal(e)
		}
	}
	ids := []string{first.ID, second.ID}
	if _, e = m.PublishEvent(ctx, c.Permit, protocol.Event{Kind: protocol.EventRunStarted, RunID: "cancel-run", MessageIDs: ids}); e != nil {
		t.Fatal(e)
	}
	if e = m.Cancel(ctx, th.ID, first.ID); e != nil {
		t.Fatal(e)
	}
	controls, e := m.ReadPendingInputs(ctx, c.Permit)
	if e != nil || len(controls) != 1 {
		t.Fatal(controls, e)
	}
	m.ConfirmInputDelivery(ctx, c.Permit, controls[0].ID)
	if _, e = m.PublishEvent(ctx, c.Permit, protocol.Event{Kind: protocol.EventRunCancelled, RunID: "cancel-run", MessageIDs: ids}); e != nil {
		t.Fatal(e)
	}
	events, e := m.ListEvents(ctx, api.EventFilter{ThreadID: th.ID})
	if e != nil {
		t.Fatal(e)
	}
	cancelledEvent := events[len(events)-1]
	if len(cancelledEvent.MessageIDs) != 1 || cancelledEvent.MessageIDs[0] != first.ID {
		t.Fatalf("retryable request incorrectly terminated: %+v", cancelledEvent.MessageIDs)
	}
	if e = m.ReleaseThread(ctx, c.Permit, api.Release{}); e != nil {
		t.Fatal(e)
	}
	next, e := m.ClaimThread(ctx, th.ID, "next", time.Minute)
	if e != nil || len(next.Inputs) != 1 || next.Inputs[0].ID != second.ID {
		t.Fatalf("later accepted input lost: %+v %v", next, e)
	}
	if _, e = m.PublishEvent(ctx, next.Permit, protocol.Event{Kind: protocol.EventRunCompleted, RunID: "next-run", MessageIDs: []string{second.ID}}); e != nil {
		t.Fatal(e)
	}
	if e = m.ReleaseThread(ctx, next.Permit, api.Release{}); e != nil {
		t.Fatal(e)
	}
	state, _ := m.GetThread(ctx, th.ID)
	if state.State != api.Idle {
		t.Fatal(state)
	}
}
func TestSpontaneousRunCancellationTerminatesAcceptedInput(t *testing.T) {
	m, th, in := setup(t)
	c, _ := m.ClaimThread(ctx, th.ID, "w", time.Minute)
	m.ConfirmInputDelivery(ctx, c.Permit, in.ID)
	if _, e := m.PublishEvent(ctx, c.Permit, protocol.Event{Kind: protocol.EventRunCancelled, RunID: "shutdown", MessageIDs: []string{in.ID}}); e != nil {
		t.Fatal(e)
	}
	if e := m.ReleaseThread(ctx, c.Permit, api.Release{}); e != nil {
		t.Fatal(e)
	}
	got, _ := m.GetThread(ctx, th.ID)
	if got.State != api.Idle {
		t.Fatal(got)
	}
}

func TestRestoreFailureBlockSurvivesCrashBeforeAck(t *testing.T) {
	m, th, in := setup(t)
	first, _ := m.ClaimThread(ctx, th.ID, "first", time.Minute)
	m.ConfirmInputDelivery(ctx, first.Permit, in.ID)
	b := &protocol.Block{RunID: "run", CheckpointID: "cp", InterruptID: "int"}
	if e := m.ReleaseThread(ctx, first.Permit, api.Release{Block: b}); e != nil {
		t.Fatal(e)
	}
	resume, e := m.ResumeFromBlock(ctx, th.ID, protocol.Input{Kind: protocol.InputResume, Resume: &protocol.Resume{RunID: b.RunID, CheckpointID: b.CheckpointID, InterruptID: b.InterruptID}})
	if e != nil {
		t.Fatal(e)
	}
	failed, e := m.ClaimThread(ctx, th.ID, "fails", 20*time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = m.PublishEvent(ctx, failed.Permit, protocol.Event{Kind: protocol.EventRunFailed, RunID: b.RunID, MessageIDs: []string{resume.ID}, Block: b, Error: "checkpoint unavailable"}); e != nil {
		t.Fatal(e)
	}
	time.Sleep(25 * time.Millisecond)
	if _, e = m.ClaimThread(ctx, th.ID, "recover", time.Minute); !errors.Is(e, api.ErrConflict) {
		t.Fatal(e)
	}
	state, e := m.GetThread(ctx, th.ID)
	if e != nil || state.State != api.Blocked || state.Block == nil || state.Block.CheckpointID != b.CheckpointID {
		t.Fatalf("lost restoration metadata: %+v %v", state, e)
	}
}

func TestPendingCancellationGroupsResumeAndNewInput(t *testing.T) {
	m, th, in := setup(t)
	c, _ := m.ClaimThread(ctx, th.ID, "setup", time.Minute)
	m.ConfirmInputDelivery(ctx, c.Permit, in.ID)
	b := &protocol.Block{RunID: "resume-run", CheckpointID: "cp", InterruptID: "int"}
	m.ReleaseThread(ctx, c.Permit, api.Release{Block: b})
	resume, e := m.ResumeFromBlock(ctx, th.ID, protocol.Input{Kind: protocol.InputResume, Resume: &protocol.Resume{RunID: b.RunID, CheckpointID: b.CheckpointID, InterruptID: b.InterruptID}})
	if e != nil {
		t.Fatal(e)
	}
	user, e := m.SubmitInput(ctx, th.ID, protocol.Input{Kind: protocol.InputUser, Text: "new request"})
	if e != nil {
		t.Fatal(e)
	}
	if e = m.Cancel(ctx, th.ID, ""); e != nil {
		t.Fatal(e)
	}
	events, e := m.ListEvents(ctx, api.EventFilter{ThreadID: th.ID})
	if e != nil || len(events) != 2 {
		t.Fatal(events, e)
	}
	byInput := map[string]protocol.Event{}
	for _, event := range events {
		for _, id := range event.MessageIDs {
			byInput[id] = event
		}
	}
	if byInput[resume.ID].RunID != b.RunID || byInput[user.ID].RunID == "" || byInput[user.ID].RunID == b.RunID {
		t.Fatal(events)
	}
	if events[1].Sequence != events[0].Sequence+1 {
		t.Fatal(events)
	}
	if e = m.Cancel(ctx, th.ID, ""); e != nil {
		t.Fatal(e)
	}
	again, _ := m.ListEvents(ctx, api.EventFilter{ThreadID: th.ID})
	if len(again) != 2 {
		t.Fatal("repeated cancel duplicated disposition", again)
	}
}

func TestCompletedCutoffDoesNotCancelNewRun(t *testing.T) {
	m, th, oldInput := setup(t)
	first, _ := m.ClaimThread(ctx, th.ID, "first", time.Minute)
	m.ConfirmInputDelivery(ctx, first.Permit, oldInput.ID)
	m.PublishEvent(ctx, first.Permit, protocol.Event{Kind: protocol.EventRunCompleted, RunID: "old-run", MessageIDs: []string{oldInput.ID}})
	m.ReleaseThread(ctx, first.Permit, api.Release{})
	newInput, e := m.SubmitInput(ctx, th.ID, protocol.Input{Kind: protocol.InputUser, Text: "newer"})
	if e != nil {
		t.Fatal(e)
	}
	current, _ := m.ClaimThread(ctx, th.ID, "current", time.Minute)
	m.ConfirmInputDelivery(ctx, current.Permit, newInput.ID)
	m.PublishEvent(ctx, current.Permit, protocol.Event{Kind: protocol.EventRunStarted, RunID: "new-run", MessageIDs: []string{newInput.ID}})
	for range 2 {
		if e = m.Cancel(ctx, th.ID, oldInput.ID); e != nil {
			t.Fatal(e)
		}
	}
	pending, e := m.ReadPendingInputs(ctx, current.Permit)
	if e != nil || len(pending) != 0 {
		t.Fatalf("old cutoff generated control for new run: %+v %v", pending, e)
	}
	thread, e := m.GetThread(ctx, th.ID)
	if e != nil || thread.State != api.Running {
		t.Fatal(thread, e)
	}
}
func TestCurrentBlockedInputRemainsCancelable(t *testing.T) {
	m, th, in := setup(t)
	c, _ := m.ClaimThread(ctx, th.ID, "w", time.Minute)
	m.ConfirmInputDelivery(ctx, c.Permit, in.ID)
	b := &protocol.Block{RunID: "blocked", CheckpointID: "cp", InterruptID: "int"}
	m.PublishEvent(ctx, c.Permit, protocol.Event{Kind: protocol.EventBlocked, RunID: b.RunID, MessageIDs: []string{in.ID}, Block: b})
	m.ReleaseThread(ctx, c.Permit, api.Release{Block: b})
	if e := m.Cancel(ctx, th.ID, in.ID); e != nil {
		t.Fatal(e)
	}
	thread, e := m.GetThread(ctx, th.ID)
	if e != nil || thread.State != api.Ready || thread.Block != nil {
		t.Fatal(thread, e)
	}
	next, e := m.ClaimThread(ctx, th.ID, "canceller", time.Minute)
	if e != nil || len(next.Inputs) != 1 || next.Inputs[0].Kind != protocol.InputCancel {
		t.Fatal(next, e)
	}
}
