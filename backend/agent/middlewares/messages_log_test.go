package middlewares

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestMessagesLogWritesOnlyNewMessages(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "agent-messages.log")
	mw := NewMessagesLog(logPath)

	first := &adk.ChatModelAgentState{
		Messages: []*schema.Message{schema.UserMessage("hi")},
	}
	_, _, err := mw.BeforeModelRewriteState(context.Background(), first, nil)
	if err != nil {
		t.Fatal(err)
	}

	second := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			schema.UserMessage("hi"),
			schema.AssistantMessage("hello", nil),
			schema.UserMessage("next"),
		},
	}
	_, _, err = mw.BeforeModelRewriteState(context.Background(), second, nil)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Count(body, `"content":"hi"`) != 1 {
		t.Fatalf("messages log duplicated first message: %s", body)
	}
	if !strings.Contains(body, `"type":"assistant"`) || !strings.Contains(body, `"content":"hello"`) {
		t.Fatalf("messages log missing assistant message: %s", body)
	}
	if !strings.Contains(body, `"type":"user"`) || !strings.Contains(body, `"content":"next"`) {
		t.Fatalf("messages log missing new user message: %s", body)
	}
}
