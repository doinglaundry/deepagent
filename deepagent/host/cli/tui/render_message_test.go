package tui

import (
	"strings"
	"testing"
)

func TestBuiltinHelpMentionsTodos(t *testing.T) {
	if !strings.Contains(builtinHelp(), "/todos") {
		t.Errorf("builtinHelp() missing /todos entry:\n%s", builtinHelp())
	}
}

// Assistant render must not start with newline: the marker and body share line 1.
func TestRenderMessage_AssistantPrefixSameLine(t *testing.T) {
	m := &Model{}
	m.viewport.Width = 80

	const reply = "Why don't scientists trust atoms? Because they make up everything!"
	rendered := renderMarkdown(m, reply)
	out := renderMessage(m, chatMessage{Role: "assistant", Content: reply, Rendered: rendered})

	if strings.HasPrefix(out, "\n") || strings.HasPrefix(rendered, "\n") {
		t.Fatalf("assistant render starts with newline; marker and body should share line 1.\nrendered=%q\nout=%q", rendered, out)
	}
	if !strings.Contains(firstLine(out), "atoms") {
		t.Errorf("first line should hold the start of the reply; got %q", firstLine(out))
	}
}

func TestQueueCompletedTurnScrollbackAddsDivider(t *testing.T) {
	m := &Model{
		width:    32,
		messages: []chatMessage{{Role: "assistant", Content: "done"}},
	}

	queueCompletedTurnScrollback(m)

	if len(m.pendingScrollback) != 2 {
		t.Fatalf("completed turn should queue message and divider, got %#v", m.pendingScrollback)
	}
	if !strings.Contains(m.pendingScrollback[1], "──") {
		t.Fatalf("divider missing from completed turn: %q", m.pendingScrollback[1])
	}
	if live := getLiveMessages(m); len(live) != 0 {
		t.Fatalf("completed turn should leave no live messages, got %#v", live)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
