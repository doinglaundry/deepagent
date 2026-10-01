package manager

import (
	"context"
	dalmodel "eino-cli/deepagent/dal/model"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestManager_ReleaseOnlyClearsLease(t *testing.T) {
	manager, thread := releaseTestManager(t)
	released, err := manager.ReleaseThread(context.Background(), thread.ThreadID, thread.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	if released.Status != thread.Status {
		t.Fatalf("release changed lifecycle: %s -> %s", thread.Status, released.Status)
	}
	if released.LeaseToken != "" {
		t.Fatal("lease not released")
	}
}

func TestManager_BlockedRunDoesNotReplayAnOldResume(t *testing.T) {
	manager, thread := releaseTestManager(t)
	ctx := context.Background()
	err := manager.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "run", []OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"blocked","checkpoint_id":"checkpoint","interrupt_id":"first"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.ReleaseThread(ctx, thread.ThreadID, thread.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	response, err := manager.Resume(ctx, thread.ThreadID, &InputMessage{MessageType: "resume_run", Payload: []byte(`{"run_id":"run","checkpoint_id":"checkpoint","interrupt_id":"first","approval":{"approved":true}}`)})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := manager.Acquire(ctx, AcquireRequest{})
	if err != nil || claim.Lease == nil {
		t.Fatalf("claim=%+v error=%v", claim, err)
	}
	_, err = manager.AckInput(ctx, thread.ThreadID, claim.Lease.LeaseToken, "run", []int64{response.Message.MessageID})
	if err != nil {
		t.Fatal(err)
	}
	err = manager.SaveOutput(ctx, thread.ThreadID, claim.Lease.LeaseToken, "run", []OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"started"}`)}, {EventType: "run_status", Payload: []byte(`{"status":"blocked","checkpoint_id":"checkpoint","interrupt_id":"second"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.ReleaseThread(ctx, thread.ThreadID, claim.Lease.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	claim, err = manager.Acquire(ctx, AcquireRequest{})
	if err != nil || claim.Lease != nil {
		t.Fatalf("old answer scheduled again: %+v error=%v", claim, err)
	}
}

func TestManager_ResumeQueuedBeforeAckSurvivesLeaseLoss(t *testing.T) {
	m, thread := releaseTestManager(t)
	ctx := context.Background()
	err := m.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "run", []OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"blocked","checkpoint_id":"checkpoint","interrupt_id":"question"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.ReleaseThread(ctx, thread.ThreadID, thread.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Resume(ctx, thread.ThreadID, &InputMessage{MessageType: "resume_run", Payload: []byte(`{"run_id":"run","checkpoint_id":"checkpoint","interrupt_id":"question","approval":{"approved":true}}`)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Acquire(ctx, AcquireRequest{})
	if err != nil || first.Lease == nil {
		t.Fatalf("claim=%+v error=%v", first, err)
	}
	// The model emits RunStart before the input thread finishes AckInput.
	err = m.SaveOutput(ctx, thread.ThreadID, first.Lease.LeaseToken, "run", []OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"started"}`)}})
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Second)
	_, err = m.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}}, map[string]any{"lease_until": expired})
	if err != nil {
		t.Fatal(err)
	}
	second, err := m.Acquire(ctx, AcquireRequest{})
	if err != nil || second.Lease == nil || len(second.PendingMessages) != 1 {
		t.Fatalf("claim=%+v error=%v", second, err)
	}
	err = m.SaveOutput(ctx, thread.ThreadID, second.Lease.LeaseToken, "run", []OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"started"}`)}})
	if err != nil {
		t.Fatalf("checkpoint Run cannot resume: %v", err)
	}
}

func saveState(t *testing.T, m *Manager, threadID int64, token, runID, status string) {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"status": status, "checkpoint_id": "checkpoint", "interrupt_id": "question"})
	if err != nil {
		t.Fatal(err)
	}
	err = m.SaveOutput(context.Background(), threadID, token, runID, []OutputFrame{{EventType: "run_status", Payload: payload}})
	if err != nil {
		t.Fatal(err)
	}
}

func submitStateInput(t *testing.T, m *Manager, threadID int64) *dalmodel.Message {
	t.Helper()
	result, err := m.Submit(context.Background(), SubmitRequest{ThreadID: threadID, Input: &InputMessage{MessageType: "input", Payload: []byte(`{"parts":[{"type":"text","text":"hello"}]}`)}})
	if err != nil {
		t.Fatal(err)
	}
	return result.Message
}

func TestManager_RunHistoryIsIndependentOfInputDelivery(t *testing.T) {
	m, thread := releaseTestManager(t)
	ctx := context.Background()
	first := submitStateInput(t, m, thread.ThreadID)
	_, err := m.AckInput(ctx, thread.ThreadID, thread.LeaseToken, "first", []int64{first.MessageID})
	if err != nil {
		t.Fatal(err)
	}
	saveState(t, m, thread.ThreadID, thread.LeaseToken, "first", "finished")
	second := submitStateInput(t, m, thread.ThreadID)
	_, err = m.AckInput(ctx, thread.ThreadID, thread.LeaseToken, "second", []int64{second.MessageID})
	if err != nil {
		t.Fatal(err)
	}
	history, err := m.ListMessages(ctx, ListMessagesRequest{ThreadID: thread.ThreadID})
	if err != nil {
		t.Fatal(err)
	}
	if history.Runs["first"].Status != "finished" || history.Runs["second"].Status != "started" {
		t.Fatalf("lost outcomes: %+v", history.Runs)
	}
	for _, message := range history.Messages {
		if message.Status != dalmodel.MessageStatusAccepted {
			t.Fatalf("execution outcome copied into message: %+v", message)
		}
	}
	err = m.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "second", []OutputFrame{{EventType: "assistant_message", Payload: []byte(`{"parts":[{"type":"text","text":"answer"}]}`)}})
	if err != nil {
		t.Fatal(err)
	}
	history, err = m.ListMessages(ctx, ListMessagesRequest{ThreadID: thread.ThreadID})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range history.Messages {
		if message.OutputKey != nil && message.Status != "" {
			t.Fatalf("output has delivery state: %+v", message)
		}
	}
}

func TestManager_CrashOnlyRedeliversUnfinishedInput(t *testing.T) {
	m, thread := releaseTestManager(t)
	ctx := context.Background()
	done := submitStateInput(t, m, thread.ThreadID)
	_, err := m.AckInput(ctx, thread.ThreadID, thread.LeaseToken, "done", []int64{done.MessageID})
	if err != nil {
		t.Fatal(err)
	}
	saveState(t, m, thread.ThreadID, thread.LeaseToken, "done", "finished")
	unfinished := submitStateInput(t, m, thread.ThreadID)
	_, err = m.AckInput(ctx, thread.ThreadID, thread.LeaseToken, "crashed", []int64{unfinished.MessageID})
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Second)
	_, err = m.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}}, map[string]any{"lease_until": expired})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := m.Acquire(ctx, AcquireRequest{})
	if err != nil || claim.Lease == nil || len(claim.PendingMessages) != 1 {
		t.Fatalf("claim=%+v error=%v", claim, err)
	}
	replay := claim.PendingMessages[0]
	if replay.MessageID != unfinished.MessageID || replay.TriggerRunID != "" {
		t.Fatalf("wrong redelivery: %+v", replay)
	}
	history, err := m.ListMessages(ctx, ListMessagesRequest{ThreadID: thread.ThreadID})
	if err != nil {
		t.Fatal(err)
	}
	// The old Run is preserved even though its input is about to join a new one.
	runs, err := m.runs.Get(ctx, thread.ThreadID, []string{"done", "crashed"})
	if err != nil || runs["done"].Status != "finished" || runs["crashed"].Status != "interrupted" {
		t.Fatalf("runs=%+v error=%v history=%+v", runs, err, history)
	}
	for _, operation := range []func() error{
		func() error { _, err := m.Renew(ctx, thread.ThreadID, thread.LeaseToken, 1000); return err },
		func() error {
			_, err := m.AckInput(ctx, thread.ThreadID, thread.LeaseToken, "crashed", []int64{replay.MessageID})
			return err
		},
		func() error { _, err := m.ReleaseThread(ctx, thread.ThreadID, thread.LeaseToken); return err },
		func() error {
			return m.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "crashed", []OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"finished"}`)}})
		},
	} {
		err = operation()
		if !errors.Is(err, ErrLeaseMismatch) {
			t.Fatalf("old lease operation=%v", err)
		}
	}
}

func TestManager_BlockedInputWaitsAndResumeDoesNotStealLease(t *testing.T) {
	m, thread := releaseTestManager(t)
	ctx := context.Background()
	saveState(t, m, thread.ThreadID, thread.LeaseToken, "blocked", "blocked")
	ordinary := submitStateInput(t, m, thread.ThreadID)
	response, err := m.Resume(ctx, thread.ThreadID, &InputMessage{MessageType: "resume_run", Payload: []byte(`{"run_id":"blocked","checkpoint_id":"checkpoint","interrupt_id":"question","approval":{"approved":true}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if response.Thread.LeaseToken != thread.LeaseToken {
		t.Fatal("Resume stole ownership during cleanup")
	}
	claim, err := m.Acquire(ctx, AcquireRequest{})
	if err != nil || claim.Lease != nil {
		t.Fatalf("live lease acquired: %+v %v", claim, err)
	}
	_, err = m.Renew(ctx, thread.ThreadID, thread.LeaseToken, 60000)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.ReleaseThread(ctx, thread.ThreadID, thread.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	claim, err = m.Acquire(ctx, AcquireRequest{})
	if err != nil || claim.Lease == nil || len(claim.PendingMessages) != 1 || claim.PendingMessages[0].MessageID != response.Message.MessageID {
		t.Fatalf("ordinary input bypassed block: %+v %v", claim, err)
	}
	saveState(t, m, thread.ThreadID, claim.Lease.LeaseToken, "blocked", "started")
	fetched, err := m.Acquire(ctx, AcquireRequest{ThreadID: thread.ThreadID, LeaseToken: claim.Lease.LeaseToken})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range fetched.PendingMessages {
		found = found || message.MessageID == ordinary.MessageID
	}
	if !found {
		t.Fatal("queued input lost after resume")
	}
}

func TestManager_CancelAndClosePreserveDeliveryAndOwnership(t *testing.T) {
	m, thread := releaseTestManager(t)
	ctx := context.Background()
	input := submitStateInput(t, m, thread.ThreadID)
	canceled, err := m.Cancel(ctx, thread.ThreadID, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	message, err := m.findMessage(ctx, thread.ThreadID, input.MessageID)
	if err != nil || message.Status != dalmodel.MessageStatusCanceled {
		t.Fatalf("message=%+v error=%v", message, err)
	}
	fetched, err := m.Acquire(ctx, AcquireRequest{ThreadID: thread.ThreadID, LeaseToken: thread.LeaseToken})
	if err != nil || len(fetched.PendingMessages) != 1 || fetched.PendingMessages[0].MessageID != canceled.Message.MessageID {
		t.Fatalf("cancel delivery=%+v %v", fetched, err)
	}
	_, err = m.AckInput(ctx, thread.ThreadID, thread.LeaseToken, "", []int64{canceled.Message.MessageID})
	if err != nil {
		t.Fatal(err)
	}
	closing, err := m.Close(ctx, thread.ThreadID, "")
	if err != nil || closing.Thread.Status != dalmodel.ThreadStatusClosing || closing.Thread.LeaseToken != thread.LeaseToken {
		t.Fatalf("close=%+v %v", closing, err)
	}
	_, err = m.Submit(ctx, SubmitRequest{ThreadID: thread.ThreadID, Input: &InputMessage{MessageType: "input"}})
	if !errors.Is(err, ErrThreadClosed) {
		t.Fatalf("closing accepted input: %v", err)
	}
	_, err = m.ConfirmThreadClosed(ctx, thread.ThreadID, "wrong", closing.Message.MessageID)
	if !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("wrong lease closed thread: %v", err)
	}
	closed, err := m.ConfirmThreadClosed(ctx, thread.ThreadID, thread.LeaseToken, closing.Message.MessageID)
	if err != nil || closed.Thread.Status != dalmodel.ThreadStatusClosed || closed.Thread.LeaseToken != "" {
		t.Fatalf("closed=%+v %v", closed, err)
	}
	claim, err := m.Acquire(ctx, AcquireRequest{})
	if err != nil || claim.Lease != nil {
		t.Fatalf("closed thread acquired: %+v %v", claim, err)
	}
}

func TestManager_OutputFailureRollsBackRunAndConsumedInputTogether(t *testing.T) {
	m, thread := releaseTestManager(t)
	ctx := context.Background()
	message := submitStateInput(t, m, thread.ThreadID)
	err := m.SaveOutput(ctx, thread.ThreadID, thread.LeaseToken, "run", []OutputFrame{
		{EventType: "run_status", Payload: []byte(fmt.Sprintf(`{"status":"started","consumed_message_ids":["%d"]}`, message.MessageID))},
		{EventType: "run_status", Payload: []byte(`{"status":"blocked"}`)},
	})
	if err == nil {
		t.Fatal("accepted malformed blocked result")
	}
	saved, err := m.findMessage(ctx, thread.ThreadID, message.MessageID)
	if err != nil || saved.Status != dalmodel.MessageStatusPending || saved.TriggerRunID != "" {
		t.Fatalf("partial input binding=%+v %v", saved, err)
	}
	runs, err := m.runs.Get(ctx, thread.ThreadID, []string{"run"})
	if err != nil || len(runs) != 0 {
		t.Fatalf("partial run result=%+v %v", runs, err)
	}
}

func TestManager_SubmitAndReleaseCannotLoseWork(t *testing.T) {
	m, thread := releaseTestManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	locked, unlock, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- m.db.Transaction(ctx, func(txCtx context.Context) error {
			_, err := m.lockThread(txCtx, thread.ThreadID)
			if err != nil {
				return err
			}
			close(locked)
			<-unlock
			return nil
		})
	}()
	<-locked
	operations := make(chan error, 2)
	go func() {
		_, err := m.Submit(ctx, SubmitRequest{ThreadID: thread.ThreadID, Input: &InputMessage{MessageType: "input", Payload: []byte(`{}`)}})
		operations <- err
	}()
	go func() { _, err := m.ReleaseThread(ctx, thread.ThreadID, thread.LeaseToken); operations <- err }()
	select {
	case err := <-operations:
		close(unlock)
		t.Fatalf("operation bypassed lifecycle lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(unlock)
	err := <-done
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		err = <-operations
		if err != nil {
			t.Fatal(err)
		}
	}
	claim, err := m.Acquire(ctx, AcquireRequest{})
	if err != nil || claim.Lease == nil || len(claim.PendingMessages) != 1 || claim.Thread.Status != dalmodel.ThreadStatusOpen {
		t.Fatalf("work lost: %+v error=%v", claim, err)
	}
}

func TestManager_ResumeRecoveryDoesNotDropAcceptedFollowUp(t *testing.T) {
	m, thread := releaseTestManager(t)
	ctx := context.Background()
	saveState(t, m, thread.ThreadID, thread.LeaseToken, "run", "blocked")
	_, err := m.ReleaseThread(ctx, thread.ThreadID, thread.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	response, err := m.Resume(ctx, thread.ThreadID, &InputMessage{MessageType: "resume_run", Payload: []byte(`{"run_id":"run","checkpoint_id":"checkpoint","interrupt_id":"question","approval":{"approved":true}}`)})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := m.Acquire(ctx, AcquireRequest{})
	if err != nil || claim.Lease == nil {
		t.Fatalf("claim=%+v %v", claim, err)
	}
	_, err = m.AckInput(ctx, thread.ThreadID, claim.Lease.LeaseToken, "run", []int64{response.Message.MessageID})
	if err != nil {
		t.Fatal(err)
	}
	saveState(t, m, thread.ThreadID, claim.Lease.LeaseToken, "run", "started")
	followUp := submitStateInput(t, m, thread.ThreadID)
	_, err = m.AckInput(ctx, thread.ThreadID, claim.Lease.LeaseToken, "run", []int64{followUp.MessageID})
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Second)
	_, err = m.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}}, map[string]any{"lease_until": expired})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := m.Acquire(ctx, AcquireRequest{})
	if err != nil || recovered.Lease == nil || len(recovered.PendingMessages) != 2 {
		t.Fatalf("accepted follow-up lost: %+v %v", recovered, err)
	}
	if recovered.PendingMessages[0].MessageID != response.Message.MessageID || recovered.PendingMessages[1].MessageID != followUp.MessageID {
		t.Fatalf("wrong delivery order: %+v", recovered.PendingMessages)
	}
}

func TestManager_RenewCannotReviveLeaseExpiredWhileWaitingForLock(t *testing.T) {
	m, thread := releaseTestManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	until := time.Now().Add(100 * time.Millisecond)
	_, err := m.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}}, map[string]any{"lease_until": until})
	if err != nil {
		t.Fatal(err)
	}
	locked, unlock, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- m.db.Transaction(ctx, func(txCtx context.Context) error {
			_, err := m.lockThread(txCtx, thread.ThreadID)
			if err != nil {
				return err
			}
			close(locked)
			<-unlock
			return nil
		})
	}()
	<-locked
	renewed := make(chan error, 1)
	go func() { _, err := m.Renew(ctx, thread.ThreadID, thread.LeaseToken, 60000); renewed <- err }()
	time.Sleep(time.Until(until.Add(30 * time.Millisecond)))
	close(unlock)
	err = <-done
	if err != nil {
		t.Fatal(err)
	}
	err = <-renewed
	if !errors.Is(err, ErrLeaseMismatch) {
		t.Fatalf("expired ownership renewed: %v", err)
	}
}

func TestManager_RepeatedLeaseLossKeepsResumeRunIdentity(t *testing.T) {
	m, thread := releaseTestManager(t)
	ctx := context.Background()
	saveState(t, m, thread.ThreadID, thread.LeaseToken, "run", "blocked")
	_, err := m.ReleaseThread(ctx, thread.ThreadID, thread.LeaseToken)
	if err != nil {
		t.Fatal(err)
	}
	response, err := m.Resume(ctx, thread.ThreadID, &InputMessage{MessageType: "resume_run", Payload: []byte(`{"run_id":"run","checkpoint_id":"checkpoint","interrupt_id":"question","approval":{"approved":true}}`)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.Acquire(ctx, AcquireRequest{})
	if err != nil || first.Lease == nil {
		t.Fatalf("claim=%+v %v", first, err)
	}
	_, err = m.AckInput(ctx, thread.ThreadID, first.Lease.LeaseToken, "run", []int64{response.Message.MessageID})
	if err != nil {
		t.Fatal(err)
	}
	saveState(t, m, thread.ThreadID, first.Lease.LeaseToken, "run", "started")
	for i := 0; i < 2; i++ {
		expired := time.Now().Add(-time.Second)
		_, err = m.threads.Update(ctx, &dalmodel.ThreadFilter{IDs: []int64{thread.ThreadID}}, map[string]any{"lease_until": expired})
		if err != nil {
			t.Fatal(err)
		}
		claim, err := m.Acquire(ctx, AcquireRequest{})
		if err != nil || claim.Lease == nil || len(claim.PendingMessages) != 1 {
			t.Fatalf("recovery %d: %+v %v", i, claim, err)
		}
		if claim.PendingMessages[0].TriggerRunID != "run" {
			t.Fatalf("resume lost identity: %+v", claim.PendingMessages[0])
		}
	}
}

func TestManager_RunIdentityCannotOverwriteAnotherThread(t *testing.T) {
	m, first := releaseTestManager(t)
	_, second := releaseTestManager(t)
	saveState(t, m, first.ThreadID, first.LeaseToken, "shared-id", "started")
	err := m.SaveOutput(context.Background(), second.ThreadID, second.LeaseToken, "shared-id", []OutputFrame{{EventType: "run_status", Payload: []byte(`{"status":"started"}`)}})
	if err == nil {
		t.Fatal("Run identity reassigned to another Thread")
	}
	runs, err := m.runs.Get(context.Background(), first.ThreadID, []string{"shared-id"})
	if err != nil || runs["shared-id"] == nil {
		t.Fatalf("first Thread lost its Run: %v %v", runs, err)
	}
}
