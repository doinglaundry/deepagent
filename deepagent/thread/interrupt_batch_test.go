package thread

import (
	"context"
	"testing"

	"eino-cli/deepagent/core/runtime/agentthread"
	"eino-cli/deepagent/core/tools"
	eventpkg "eino-cli/deepagent/protocol/event"
	inputpkg "eino-cli/deepagent/protocol/input"
)

func TestInterruptBatchPreservesEveryApproval(t *testing.T) {
	batch := agentthread.InterruptBatchPayload{CheckpointID: "checkpoint", Items: []agentthread.InterruptBatchItem{
		{InterruptID: "first", Kind: agentthread.InterruptItemApprove, ApprovalInfo: &tools.ApprovalInfo{CallID: "call-a", ToolName: "write_file"}},
		{InterruptID: "second", Kind: agentthread.InterruptItemApprove, ApprovalInfo: &tools.ApprovalInfo{CallID: "call-b", ToolName: "execute"}},
	}}
	kind, payload, err := agentEventPayloadForOutput(agentthread.Event{Type: agentthread.EventInterruptBatchRequested, Payload: batch}, nil)
	if err != nil || kind != eventpkg.EventTypeInputRequired {
		t.Fatalf("kind=%s payload=%v err=%v", kind, payload, err)
	}
	out, ok := payload.(*eventpkg.InterruptBatchRequiredEventPayload)
	if !ok || len(out.Items) != 2 || out.Items[0].InterruptID != "first" || out.Items[1].InterruptID != "second" {
		t.Fatalf("payload=%+v", payload)
	}
	resume := inputpkg.ResumeRunPayload{InterruptID: "first", Answers: []inputpkg.ResumeAnswer{
		{InterruptID: "first", Approval: &inputpkg.ApprovalDecision{Approved: true}},
		{InterruptID: "second", Approval: &inputpkg.ApprovalDecision{Approved: false}},
	}}
	answers, err := resumeData(context.Background(), resume, nil)
	if err != nil || len(answers) != 2 {
		t.Fatalf("answers=%v err=%v", answers, err)
	}
	if answers["first"].(*tools.ApprovalResult).Approved != true || answers["second"].(*tools.ApprovalResult).Approved != false {
		t.Fatalf("answers=%v", answers)
	}
}
