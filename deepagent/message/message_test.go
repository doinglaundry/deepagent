package message

import (
	"encoding/json"
	"reflect"
	"testing"

	inputpkg "eino-cli/deepagent/protocol/input"

	"github.com/cloudwego/eino/schema"
)

func TestEinoRoundTripPreservesModelContent(t *testing.T) {
	url, data, index := "https://example.test/input", "YWJj", 37
	source := &schema.Message{
		Role: schema.Assistant, Content: "answer", Name: "agent", ToolCallID: "result-id", ToolName: "read_file", ReasoningContent: "reasoning",
		MultiContent: []schema.ChatMessagePart{{Type: schema.ChatMessagePartTypeText, Text: "provider content"}},
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "question", Extra: map[string]any{"source": "user"}},
			{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url, MIMEType: "image/png"}, Detail: schema.ImageURLDetailHigh}},
			{Type: schema.ChatMessagePartTypeAudioURL, Audio: &schema.MessageInputAudio{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: "audio/wav"}}},
			{Type: schema.ChatMessagePartTypeVideoURL, Video: &schema.MessageInputVideo{MessagePartCommon: schema.MessagePartCommon{URL: &url, MIMEType: "video/mp4"}}},
			{Type: schema.ChatMessagePartTypeFileURL, File: &schema.MessageInputFile{MessagePartCommon: schema.MessagePartCommon{URL: &url, MIMEType: "application/pdf"}, Name: "report.pdf"}},
		},
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: "thinking", Signature: "signed"}, StreamingMeta: &schema.MessageStreamingMeta{Index: 2}},
			{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageOutputImage{MessagePartCommon: schema.MessagePartCommon{URL: &url}}},
		},
		ToolCalls:    []schema.ToolCall{{Index: &index, ID: "call", Type: "function", Function: schema.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}, Extra: map[string]any{"signature": "tool-signature"}}},
		ResponseMeta: &schema.ResponseMeta{FinishReason: "tool_calls", Usage: &schema.TokenUsage{PromptTokens: 12, CompletionTokens: 4, TotalTokens: 16}},
		Extra:        map[string]any{"provider_token": int64(9007199254740993), "typed_metadata": map[string]string{"key": "value"}},
	}
	business := FromEino(source)
	business.MessageID, business.ThreadID, business.RunID, business.SenderID, business.SenderType = "9007199254740993", "thread", "run", "person", "user"
	business.Seq, business.CreatedAt = 19, 42
	roundTrip := ToEino(business)
	if !reflect.DeepEqual(source, roundTrip) {
		t.Fatalf("model conversion lost data:\nsource=%+v\nresult=%+v", source, roundTrip)
	}
	raw, err := json.Marshal(roundTrip)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	err = json.Unmarshal(raw, &fields)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"message_id", "thread_id", "run_id", "seq", "created_at", "sender_id", "sender_type", "original_parts"} {
		_, exists := fields[key]
		if exists {
			t.Fatalf("business metadata %s leaked into model message", key)
		}
	}
	if FromEino(nil) != nil || ToEino(nil) != nil {
		t.Fatal("nil conversion must remain nil")
	}
}

func TestBusinessMessageJSONPreservesIdentityAndOriginalInput(t *testing.T) {
	source := &Message{
		MessageID: "9007199254740993", ThreadID: "thread", RunID: "run", Seq: 9, CreatedAt: 42, SenderID: "person", SenderType: "user", Role: schema.User, Content: "question",
		OriginalParts: []inputpkg.MessagePart{
			{Type: inputpkg.MessagePartTypeText, Text: "question", Extra: map[string]json.RawMessage{"source": json.RawMessage(`{"id":9007199254740993}`)}},
			{Type: inputpkg.MessagePartTypeFile, URL: "https://example.test/file", Name: "notes.pdf", MIMEType: "application/pdf", Detail: "original"},
		},
	}
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var restored Message
	err = json.Unmarshal(raw, &restored)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(source, &restored) {
		t.Fatalf("business message lost identity or original input: %+v", restored)
	}
}

func TestConcatMessagesPreservesStreamingParts(t *testing.T) {
	chunks := []*Message{
		{Role: schema.Assistant, Content: "hello ", ReasoningContent: "think ", AssistantGenMultiContent: []schema.MessageOutputPart{{Type: schema.ChatMessagePartTypeText, Text: "hello ", StreamingMeta: &schema.MessageStreamingMeta{Index: 0}}}},
		{Content: "world", ReasoningContent: "more", AssistantGenMultiContent: []schema.MessageOutputPart{{Type: schema.ChatMessagePartTypeText, Text: "world", StreamingMeta: &schema.MessageStreamingMeta{Index: 0}}}, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{TotalTokens: 12}}},
	}
	result, err := ConcatMessages(chunks)
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "hello world" || result.ReasoningContent != "think more" || result.ResponseMeta.Usage.TotalTokens != 12 || len(result.AssistantGenMultiContent) != 1 || result.AssistantGenMultiContent[0].Text != "hello world" {
		t.Fatalf("stream content lost: %+v", result)
	}
}
