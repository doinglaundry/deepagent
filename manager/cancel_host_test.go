package manager_test

import (
	"context"
	host "eino-cli/host/runtime"
	"eino-cli/manager"
	"eino-cli/manager/api"
	"eino-cli/protocol"
	"testing"
	"time"
)

func TestPendingCancellationTerminatesHostWithoutWorker(t *testing.T) {
	for _, kind := range []protocol.InputKind{protocol.InputUser, protocol.InputCompact, protocol.InputResume} {
		for _, closing := range []bool{false, true} {
			name := string(kind) + "/cancel"
			if closing {
				name = string(kind) + "/close"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				m := manager.NewMemory("pending-disposition")
				defer m.Close()
				th, e := m.CreateThread(ctx, api.CreateThreadRequest{WorkDir: t.TempDir()})
				if e != nil {
					t.Fatal(e)
				}
				in := protocol.Input{ID: protocol.NewID("request"), Kind: kind, Text: "pending"}
				if kind == protocol.InputResume {
					initial, e := m.SubmitInput(ctx, th.ID, protocol.Input{Kind: protocol.InputUser, Text: "initial"})
					if e != nil {
						t.Fatal(e)
					}
					claim, e := m.ClaimThread(ctx, th.ID, "setup", time.Minute)
					if e != nil {
						t.Fatal(e)
					}
					m.ConfirmInputDelivery(ctx, claim.Permit, initial.ID)
					block := &protocol.Block{RunID: "original-run", CheckpointID: "cp", InterruptID: "int"}
					if e = m.ReleaseThread(ctx, claim.Permit, api.Release{Block: block}); e != nil {
						t.Fatal(e)
					}
					in.Resume = &protocol.Resume{RunID: block.RunID, CheckpointID: block.CheckpointID, InterruptID: block.InterruptID}
				}
				runtime := host.New(m, host.Config{SessionID: th.SessionID, ThreadID: th.ID, PollInterval: time.Millisecond})
				defer runtime.Detach()
				stream, e := runtime.StartRun(ctx, in)
				if e != nil {
					t.Fatal(e)
				}
				defer stream.Close()
				if closing {
					e = runtime.CloseThread(ctx)
				} else {
					e = stream.Cancel(ctx)
				}
				if e != nil {
					t.Fatal(e)
				}
				select {
				case event, ok := <-stream.Events:
					if !ok || event.Kind != protocol.EventRunCancelled || len(event.MessageIDs) != 1 || event.MessageIDs[0] != in.ID {
						t.Fatalf("missing request terminal: %+v open=%v", event, ok)
					}
					if kind == protocol.InputResume && event.RunID != "original-run" {
						t.Fatal(event.RunID)
					}
				case <-time.After(250 * time.Millisecond):
					t.Fatal("Host waited forever for cancelled pending input")
				}
				select {
				case _, open := <-stream.Events:
					if open {
						t.Fatal("stream did not finish")
					}
				case <-time.After(250 * time.Millisecond):
					t.Fatal("terminal did not close stream")
				}
				durable, e := m.ListEvents(ctx, api.EventFilter{ThreadID: th.ID})
				if e != nil || len(durable) != 1 || durable[0].Sequence == 0 {
					t.Fatal(durable, e)
				}
			})
		}
	}
}
