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
	logPath := filepath.Join(t.TempDir(), "agent-messages.md")
	mw := NewMessagesLog(logPath)

	first := &adk.ChatModelAgentState{
		Messages: []*schema.Message{schema.UserMessage("hi")},
	}
	_, _, err := mw.AfterModelRewriteState(context.Background(), first, nil)
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
	_, _, err = mw.AfterModelRewriteState(context.Background(), second, nil)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if strings.Count(body, "hi") != 1 {
		t.Fatalf("messages log duplicated first message: %s", body)
	}
	if !strings.Contains(body, "· assistant") || !strings.Contains(body, "hello") {
		t.Fatalf("messages log missing assistant message: %s", body)
	}
	if !strings.Contains(body, "· user") || !strings.Contains(body, "next") {
		t.Fatalf("messages log missing new user message: %s", body)
	}
	if !strings.Contains(body, "```text\nnext\n```") || !strings.Contains(body, "\n---\n") {
		t.Fatalf("messages log missing markdown structure: %s", body)
	}
}

func TestMessagesLogKeepsMultilineContentReadable(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "agent-messages.md")
	mw := NewMessagesLog(logPath)
	content := "<memory>\nUser Context:\n```"
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{schema.SystemMessage(content)},
	}

	_, _, err := mw.AfterModelRewriteState(context.Background(), state, nil)
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "````text\n<memory>\nUser Context:\n```\n````") {
		t.Fatalf("messages log should preserve multiline content with safe fence: %s", body)
	}
}
