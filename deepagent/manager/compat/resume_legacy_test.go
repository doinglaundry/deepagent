package manager

import (
	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	"testing"
	"time"
)

func TestLegacyQueuedResumeRecoversKindFromCorrelatedDurableBlock(t *testing.T) {
	for _, kind := range []string{"approval", "clarification"} {
		t.Run(kind, func(t *testing.T) {
			m, th, input := setup(t)
			claim, err := m.ClaimThread(ctx, th.ID, "old", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			// Put the correlated block beyond one event page.
			for i := 0; i < 65; i++ {
				if _, err := m.PublishEvent(ctx, claim.Permit, protocol.Event{Kind: protocol.EventText, RunID: "run", Text: "history"}); err != nil {
					t.Fatal(err)
				}
			}
			block := &protocol.Block{Kind: kind, RunID: "run", CheckpointID: "cp", InterruptID: "gate"}
			if _, err := m.PublishEvent(ctx, claim.Permit, protocol.Event{Kind: protocol.EventBlocked, RunID: "run", MessageIDs: []string{input.ID}, Block: block}); err != nil {
				t.Fatal(err)
			}
			if err := m.ReleaseThread(ctx, claim.Permit, api.Release{Block: block}); err != nil {
				t.Fatal(err)
			}
			answer, err := m.ResumeFromBlock(ctx, th.ID, protocol.Input{Kind: protocol.InputResume, Resume: &protocol.Resume{RunID: "run", CheckpointID: "cp", InterruptID: "gate", Answer: "answer", Approved: true}})
			if err != nil {
				t.Fatal(err)
			}
			// Simulate a queued row written by the version predating Resume.Kind.
			_, err = m.store.update(ctx, th.ID, func(r *record) error {
				for i := range r.Inputs {
					if r.Inputs[i].Input.ID == answer.ID {
						r.Inputs[i].Input.Resume.Kind = ""
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			next, err := m.ClaimThread(ctx, th.ID, "new", time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			check := func(inputs []protocol.Input) {
				t.Helper()
				if len(inputs) != 1 || inputs[0].ID != answer.ID || inputs[0].Resume.Kind != kind || inputs[0].Resume.Answer != "answer" || !inputs[0].Resume.Approved {
					t.Fatalf("resume changed or not hydrated: %+v", inputs)
				}
			}
			check(next.Inputs)
			if next.Thread.Block != nil {
				t.Fatal("claim incorrectly restored blocked state")
			}
			pending, err := m.ReadPendingInputs(ctx, next.Permit)
			if err != nil {
				t.Fatal(err)
			}
			check(pending)
		})
	}
}

func TestLegacyResumeNeverUsesUncorrelatedBlock(t *testing.T) {
	for _, mismatch := range []string{"thread", "run", "checkpoint", "interrupt"} {
		t.Run(mismatch, func(t *testing.T) {
			m := NewMemory("test")
			store := m.store.(*memoryStore)
			event := protocol.Event{Sequence: 1, ThreadID: "thread", RunID: "run", Kind: protocol.EventBlocked, Block: &protocol.Block{Kind: "approval", RunID: "run", CheckpointID: "cp", InterruptID: "gate"}}
			switch mismatch {
			case "thread":
				event.ThreadID = "other"
			case "run":
				event.RunID = "other"
				event.Block.RunID = "other"
			case "checkpoint":
				event.Block.CheckpointID = "other"
			case "interrupt":
				event.Block.InterruptID = "other"
			}
			store.log = []protocol.Event{event}
			source := protocol.Input{ID: "answer", Kind: protocol.InputResume, Resume: &protocol.Resume{RunID: "run", CheckpointID: "cp", InterruptID: "gate", Approved: true}}
			r := &record{Thread: api.Thread{ID: "thread"}, Inputs: []delivery{{Input: source}}}
			inputs, err := m.deliver(ctx, r)
			if err != nil || len(inputs) != 1 || inputs[0].Resume.Kind != "" {
				t.Fatalf("uncorrelated block supplied kind: inputs=%+v err=%v", inputs, err)
			}
		})
	}
}

func TestModernResumeDoesNotReadLegacyEvents(t *testing.T) {
	m := NewMemory("test")
	// A closed event store would fail if delivery unnecessarily queried it.
	m.store.(*memoryStore).closed = true
	input := protocol.Input{ID: "answer", Kind: protocol.InputResume, Resume: &protocol.Resume{Kind: "clarification", RunID: "run", CheckpointID: "cp", InterruptID: "gate"}}
	inputs, err := m.deliver(ctx, &record{Thread: api.Thread{ID: "thread"}, Inputs: []delivery{{Input: input}}})
	if err != nil || len(inputs) != 1 || inputs[0].Resume.Kind != "clarification" {
		t.Fatalf("inputs=%+v err=%v", inputs, err)
	}
}
