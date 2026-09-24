package thread

import (
	"encoding/json"
	"reflect"
	"testing"

	"eino-cli/deepagent/core/runtime/agentthread"
	"eino-cli/deepagent/core/tools"
	eventpkg "eino-cli/deepagent/protocol/event"
	"github.com/cloudwego/eino/schema"
)

func TestThreadAdapter_InputConsumedPreservesIndividualIdentityAndMedia(t *testing.T) {
	first, second := schema.UserMessage("first"), schema.UserMessage("describe")
	attachAttribute(first, MessageAttribute{MessageID: "one"})
	attachAttribute(second, MessageAttribute{MessageID: "two", SenderID: "person", SenderType: "user"})
	url := "https://example.test/image.png"
	second.UserInputMultiContent = []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeText, Text: "describe"}, {Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url, MIMEType: "image/png"}}}}
	kind, payload, err := agentEventPayloadForOutput(agentthread.Event{Type: agentthread.EventInputConsumed, Payload: agentthread.InputConsumedPayload{Message: second}, ConsumedInputs: []*schema.Message{first, second}}, nil)
	if err != nil || kind != eventpkg.EventTypeInputConsumed {
		t.Fatalf("kind=%s err=%v", kind, err)
	}
	message := payload.(*eventpkg.MessageEventPayload)
	if message.MessageID == nil || *message.MessageID != "two" || message.Sender == nil || message.Sender.SenderID != "person" {
		t.Fatalf("identity=%+v", message)
	}
	if len(message.Parts) != 2 || message.Parts[1].URL != url || message.Parts[1].MIMEType != "image/png" {
		t.Fatalf("media lost: %+v", message.Parts)
	}
	if !reflect.DeepEqual(message.ConsumedMessageIDs, []string{"one", "two"}) {
		t.Fatal("run ownership metadata lost")
	}
}

func TestThreadAdapter_FollowUpIncludesQuestion(t *testing.T) {
	payload := followUpRequiredPayload(agentthread.FollowUpRequestedPayload{
		InterruptID: "interrupt-1", CheckpointID: "checkpoint-1",
		Info: &tools.FollowUpInfo{Question: "选择哪个目录？", Questions: []string{"src", "docs"}},
	})
	var info struct {
		Question  string   `json:"question"`
		Questions []string `json:"questions"`
	}
	if err := json.Unmarshal(payload.Info, &info); err != nil {
		t.Fatal(err)
	}
	if info.Question != "选择哪个目录？" || !reflect.DeepEqual(info.Questions, []string{"src", "docs"}) {
		t.Fatalf("follow-up lost question or choices: %s", payload.Info)
	}
	if payload.InterruptID != "interrupt-1" || payload.CheckpointID != "checkpoint-1" {
		t.Fatalf("follow-up lost resume identity: %+v", payload)
	}
}

func TestThreadAdapter_ApprovalPreservesCallIdentity(t *testing.T) {
	payload := convertApprovalRequiredPayload(agentthread.ApprovalRequiredPayload{InterruptID: "interrupt", CheckpointID: "checkpoint", ApprovalInfo: &tools.ApprovalInfo{CallID: "call", ToolName: "execute", ArgumentsInJSON: `{"command":"pwd"}`}})
	if payload.ToolCallID != "call" || payload.InterruptID != "interrupt" || payload.CheckpointID != "checkpoint" || payload.ToolName != "execute" || payload.ArgumentsJSON == nil {
		t.Fatalf("approval identity lost: %+v", payload)
	}
}
