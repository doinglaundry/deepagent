package middlewares

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"

	runtimecontext "eino-cli/backend/runtime/context"
)

func TestMessagesLogWritesOnlyNewMessages(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "agent-messages.md")
	mw := NewMessagesLog(logPath, "")

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
	mw := NewMessagesLog(logPath, "")
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

func TestMessagesLogUsesToolCallAsContent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "agent-messages.md")
	mw := NewMessagesLog(logPath, "")
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "call-1", Function: schema.FunctionCall{Name: "shell.execute", Arguments: `{"cmd":"pwd"}`}},
			},
		}},
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
	if !strings.Contains(body, "tool: shell.execute") || !strings.Contains(body, `{"cmd":"pwd"}`) {
		t.Fatalf("messages log missing tool call body: %s", body)
	}
}

func TestMessagesLogIncludesReasoningContent(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "agent-messages.md")
	mw := NewMessagesLog(logPath, "")
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{{
			Role:             schema.Assistant,
			ReasoningContent: "I need to inspect the config first.",
			Content:          "The config is ready.",
		}},
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
	for _, want := range []string{
		"reasoning:\nI need to inspect the config first.",
		"content:\nThe config is ready.",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("messages log missing %q: %s", want, body)
		}
	}
}

func TestMessagesLogWritesTranscriptJSONL(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent-messages.md")
	transcriptDir := filepath.Join(dir, "transcripts")
	mw := NewMessagesLog(logPath, transcriptDir)
	ctx := runtimecontext.WithSessionID(context.Background(), "session/1")
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{schema.UserMessage("remember this")},
	}

	_, _, err := mw.AfterModelRewriteState(ctx, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(transcriptDir, "session1.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var entry transcriptMessage
	if err := json.Unmarshal(bytesTrimRightNewline(data), &entry); err != nil {
		t.Fatal(err)
	}
	if entry.Role != "user" || entry.Content != "remember this" {
		t.Fatalf("unexpected transcript entry: %#v", entry)
	}
}

func bytesTrimRightNewline(data []byte) []byte {
	return []byte(strings.TrimRight(string(data), "\n"))
}
