package thread

import (
	"encoding/json"
	"testing"

	"eino-cli/deepagent/manager/api"
	"eino-cli/deepagent/protocol"
	inputpkg "eino-cli/deepagent/protocol/input"
)

func TestTransportInputPreservesMultimodalIdentityAndPlanMode(t *testing.T) {
	owner := api.Thread{ID: "thread", SessionID: "session", PlanMode: true}
	in := protocol.Input{ID: "message", ThreadID: "thread", SessionID: "session", Kind: protocol.InputUser, Text: "inspect", Parts: []protocol.Part{{Type: "image_url", URL: "data:image/png;base64,a", MIMEType: "image/png"}, {Type: "audio", URL: "audio", MIMEType: "audio/wav"}, {Type: "file_url", URL: "file", MIMEType: "application/pdf"}}}
	message, err := transportInput(in, owner)
	if err != nil {
		t.Fatal(err)
	}
	var user inputpkg.UserMessage
	if err = json.Unmarshal(message.Payload, &user); err != nil {
		t.Fatal(err)
	}
	if message.ID != "message" || message.Type != inputpkg.MessageTypeInput || message.Sender.Type != "user" || user.Mode != inputpkg.UserMessageModeImplPlan || len(user.Parts) != 4 {
		t.Fatalf("message=%+v payload=%+v", message, user)
	}
	if user.Parts[0].Text != "inspect" || user.Parts[1].Type != inputpkg.MessagePartTypeImage || user.Parts[1].URL != in.Parts[0].URL || user.Parts[2].MIMEType != "audio/wav" || user.Parts[3].Type != inputpkg.MessagePartTypeFile {
		t.Fatalf("parts=%+v", user.Parts)
	}
	in.ThreadID = "other"
	if _, err = transportInput(in, owner); err == nil {
		t.Fatal("foreign input accepted")
	}
}
func TestTransportResumePreservesCorrelationAndTypedAnswer(t *testing.T) {
	for _, kind := range []string{"approval", "clarification"} {
		t.Run(kind, func(t *testing.T) {
			block := &protocol.Block{RunID: "run", CheckpointID: "checkpoint", InterruptID: "interrupt", Kind: kind}
			owner := api.Thread{ID: "thread", Block: block}
			in := protocol.Input{ID: "answer", Kind: protocol.InputResume, Resume: &protocol.Resume{RunID: "run", CheckpointID: "checkpoint", InterruptID: "interrupt", Approved: false, Answer: "explanation"}}
			message, err := transportInput(in, owner)
			if err != nil {
				t.Fatal(err)
			}
			var data inputpkg.ResumeRunPayload
			if err = json.Unmarshal(message.Payload, &data); err != nil {
				t.Fatal(err)
			}
			if data.RunID != "run" || data.CheckpointID != "checkpoint" || data.InterruptID != "interrupt" || len(data.ConsumedMessageIDs) != 1 || data.ConsumedMessageIDs[0] != "answer" {
				t.Fatalf("lost correlation: %+v", data)
			}
			if kind == "approval" {
				if data.Approval == nil || data.Approval.Approved || data.Approval.Reason != "explanation" {
					t.Fatal("denial changed")
				}
			} else {
				if data.Interrupt == nil || data.Interrupt.Kind != "follow_up" {
					t.Fatal("missing typed follow-up")
				}
				var answer map[string]string
				if err = json.Unmarshal(data.Interrupt.Data, &answer); err != nil || answer["user_answer"] != "explanation" {
					t.Fatal("answer lost")
				}
			}
			owner.Block = nil
			in.Resume.Kind = kind
			if _, err = transportInput(in, owner); err != nil {
				t.Fatalf("new worker lost resume kind: %v", err)
			}
			owner.Block = block
			in.Resume.InterruptID = "other"
			if _, err = transportInput(in, owner); err == nil {
				t.Fatal("uncorrelated resume accepted")
			}
		})
	}
}
