package middleware

import (
	"context"
	"testing"

	messagepkg "eino-cli/deepagent/message"
	"github.com/cloudwego/eino/schema"
)

func TestComputer_KeepLatestScreenshotWithoutChangingHistory(t *testing.T) {
	image := schema.MessageInputPart{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{MIMEType: "image/png"}}}
	text := schema.MessageInputPart{Type: schema.ChatMessagePartTypeText, Text: "old observation"}
	messages := []*messagepkg.Message{
		nil,
		{Role: schema.Tool, ToolName: "browser_observe", ToolCallID: "old", UserInputMultiContent: []schema.MessageInputPart{text, image}},
		{Role: schema.Tool, ToolName: "image", ToolCallID: "other", UserInputMultiContent: []schema.MessageInputPart{image}},
		{Role: schema.Tool, ToolName: "computer_observe", ToolCallID: "new", UserInputMultiContent: []schema.MessageInputPart{image}},
		{Role: schema.Tool, ToolName: "browser_click", ToolCallID: "text-only", Content: "Origin changed"},
		{Role: schema.User, UserInputMultiContent: []schema.MessageInputPart{image}},
	}
	middleware := NewComputer(nil)
	filtered, err := middleware.ModifyModelRequest(context.Background(), nil, messages, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered[1].UserInputMultiContent) != 1 || filtered[1].UserInputMultiContent[0].Text != text.Text {
		t.Fatalf("old screenshot removed its text: %+v", filtered[1])
	}
	if filtered[0] != nil || filtered[2] != messages[2] || filtered[3] != messages[3] || filtered[4] != messages[4] || filtered[5] != messages[5] {
		t.Fatal("newest screenshot, unrelated images or text-only result changed")
	}
	if len(messages[1].UserInputMultiContent) != 2 || messages[1].UserInputMultiContent[1].Image == nil {
		t.Fatal("history mutated")
	}
}
