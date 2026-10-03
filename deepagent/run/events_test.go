package run

import (
	"context"
	"testing"
	"time"

	"eino-cli/deepagent/graph/execution"
	"eino-cli/deepagent/graph/tools"

	"github.com/cloudwego/eino/compose"
)

func TestThreadInterruptEventContracts(t *testing.T) {
	approval := &tools.ApprovalInfo{}
	question := &tools.FollowUpInfo{Question: "Which format?"}
	contexts := []*compose.InterruptCtx{
		{ID: "approve", Info: approval},
		{ID: "question", Info: question},
		{ID: "custom", Info: "custom data"},
	}
	for _, tc := range []struct {
		name     string
		contexts []*compose.InterruptCtx
		kind     EventType
	}{
		{"batch", contexts, EventInterruptBatchRequested},
		{"approve", contexts[:1], EventApproveRequested},
		{"custom", contexts[2:], EventInterrupted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := make(chan Event, 8)
			r, _ := New(context.Background(), "run", Config{Events: events, Graph: execution.Config{ThreadID: "thread"}, Resume: &execution.RunOptions{CheckpointID: "old", WriteToCheckpointID: "new"}})
			info := &compose.InterruptInfo{InterruptContexts: tc.contexts}
			err := r.EmitBlocked(context.Background(), info)
			if err != nil {
				t.Fatal(err)
			}
			event := <-events
			if event.Type != tc.kind {
				t.Fatalf("kind=%s", event.Type)
			}
			switch p := event.Payload.(type) {
			case InterruptBatchPayload:
				if p.CheckpointID != "new" || len(p.Items) != 3 {
					t.Fatalf("batch=%+v", p)
				}
				if p.Items[0].ApprovalInfo != approval || p.Items[1].FollowUpInfo != question || p.Items[2].Info != "custom data" {
					t.Fatalf("lost typed data: %+v", p.Items)
				}
				for i, kind := range []InterruptItemKind{InterruptItemApprove, InterruptItemFollowUp, InterruptItemCustom} {
					if p.Items[i].Kind != kind || p.Items[i].InterruptID != contexts[i].ID || p.Items[i].InfoType == "" {
						t.Fatalf("item=%+v", p.Items[i])
					}
				}
			case ApprovalRequiredPayload:
				if p.ApprovalInfo != approval || p.InterruptID != "approve" || p.CheckpointID != "new" {
					t.Fatalf("edit=%+v", p)
				}
			case InterruptedPayload:
				if p.InfoType != "string" || p.Info != "custom data" || p.InterruptID != "custom" {
					t.Fatalf("custom=%+v", p)
				}
			}
			localEvent := <-events
			if localEvent.Type != EventInterruptInfo || localEvent.Payload != info {
				t.Fatalf("raw info=%+v", localEvent)
			}
			if len(events) != 0 {
				t.Fatal("duplicate individual block events")
			}
		})
	}
}

func TestThreadExternalInterruptCopiesCorrelation(t *testing.T) {
	events := make(chan Event, 4)
	r, _ := New(context.Background(), "run", Config{Events: events, Graph: execution.Config{ThreadID: "thread"}})
	timeout := 200 * time.Millisecond
	metadata := map[string]string{"reason": "handoff"}
	r.RequestInterrupt(InterruptOptions{Timeout: &timeout, Metadata: metadata})
	r.interruptTimer.Stop()
	metadata["reason"] = "mutated"
	timeout = time.Second
	err := r.EmitBlocked(context.Background(), &compose.InterruptInfo{})
	if err != nil {
		t.Fatal(err)
	}
	event := <-events
	p := event.Payload.(InterruptedPayload)
	if event.Type != EventInterrupted || p.Source != "external" || p.Metadata["reason"] != "handoff" || p.TimeoutMS != 200 || p.CheckpointID != "run" {
		t.Fatalf("external=%+v", event)
	}
}
