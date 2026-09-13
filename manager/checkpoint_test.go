package manager

import (
	"eino-cli/manager/api"
	"errors"
	"testing"
	"time"
)

func TestCheckpointRejectsFormerOwnerAfterReclaim(t *testing.T) {
	m, th, _ := setup(t)
	old, e := m.ClaimThread(ctx, th.ID, "old", 20*time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.PutThreadCheckpoint(ctx, old.Permit, "checkpoint", []byte("old checkpoint")); e != nil {
		t.Fatal(e)
	}
	time.Sleep(25 * time.Millisecond)
	next, e := m.ClaimThread(ctx, th.ID, "new", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if e = m.PutThreadCheckpoint(ctx, next.Permit, "checkpoint", []byte("new checkpoint")); e != nil {
		t.Fatal(e)
	}
	if e = m.PutThreadCheckpoint(ctx, old.Permit, "checkpoint", []byte("stale overwrite")); !errors.Is(e, api.ErrPermitLost) {
		t.Fatal(e)
	}
	stored, e := m.GetCheckpoint(ctx, "checkpoint")
	if e != nil || string(stored) != "new checkpoint" {
		t.Fatalf("checkpoint overwritten: %q %v", stored, e)
	}
}
