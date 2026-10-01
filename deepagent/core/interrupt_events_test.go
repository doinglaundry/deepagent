package deepagents

import (
	"context"
	"testing"
	"time"

	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
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
			r := &Run{runID: "run", owner: &Thread{ThreadID: "thread", events: events}, resume: &ResumeRunOptions{CheckpointID: "old", WriteToCheckpointID: "new"}}
			info := &compose.InterruptInfo{InterruptContexts: tc.contexts}
			{
				err := r.emitBlocked(context.Background(), info)
				if err != nil {
					t.Fatal(err)
				}
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
			{
				event := <-events
				if event.Type != EventInterruptInfo || event.Payload != info {
					t.Fatalf("raw info=%+v", event)
				}
			}
			if len(events) != 0 {
				t.Fatal("duplicate individual block events")
			}
		})
	}
}

func TestThreadExternalInterruptCopiesCorrelation(t *testing.T) {
	events := make(chan Event, 4)
	r := &Run{runID: "run", owner: &Thread{ThreadID: "thread", events: events}, cancel: func(error) {}}
	timeout := 200 * time.Millisecond
	metadata := map[string]string{"reason": "handoff"}
	r.requestInterrupt(InterruptOptions{Timeout: &timeout, Metadata: metadata})
	r.interruptTimer.Stop()
	metadata["reason"] = "mutated"
	timeout = time.Second
	{
		err := r.emitBlocked(context.Background(), &compose.InterruptInfo{})
		if err != nil {
			t.Fatal(err)
		}
	}
	event := <-events
	p := event.Payload.(InterruptedPayload)
	if event.Type != EventInterrupted || p.Source != "external" || p.Metadata["reason"] != "handoff" || p.TimeoutMS != 200 || p.CheckpointID != "run" {
		t.Fatalf("external=%+v", event)
	}
}

func TestThreadExternalInterruptTimeoutRetainsMetadata(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{})
	m := &legacyParityScriptedModel{stream: func(ctx context.Context, _ int, _ []*schema.Message) *schema.StreamReader[*schema.Message] {
		reader, writer := schema.Pipe[*schema.Message](0)
		go func() { defer writer.Close(); close(started); <-ctx.Done() }()
		return reader
	}}
	events := make(chan Event, 32)
	thread := newTestThread("thread", &RunConfig{Agent: Config{Model: m, CheckpointStore: &legacyParityMemoryCheckpoints{}}}, events, ThreadOptions{})
	accepted, err := thread.SubmitInput(ctx, schema.UserMessage("wait"))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	timeout := 10 * time.Millisecond
	thread.InterruptRun(InterruptOptions{Timeout: &timeout, Metadata: map[string]string{"reason": "handoff"}})
	thread.InterruptRun(InterruptOptions{Timeout: &timeout, Metadata: map[string]string{"reason": "handoff"}})
	{
		err := accepted.RunHandle.Wait(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	found := false
	for len(events) > 0 {
		event := <-events
		if event.Type == EventError {
			t.Fatalf("external timeout became error: %+v", event)
		}
		if event.Type == EventInterrupted {
			p := event.Payload.(InterruptedPayload)
			if p.Source != "external" || p.Metadata["reason"] != "handoff" || p.TimeoutMS != 10 {
				t.Fatalf("lost correlation: %+v", p)
			}
			found = true
		}
	}
	if !found || thread.CurrentRun() != nil {
		t.Fatal("missing terminal interrupt or active run retained")
	}
}
