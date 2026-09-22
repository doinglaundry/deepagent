package tui

import (
	"strings"
	"testing"
	"time"
)

// Long-running turn (≥ 2s) → handleDone pushes both the assistant body
// and a thinking-summary, in that order. Past-tense verb wins; the live
// indicator's present tense is no longer relevant.
func TestHandleDone_PushesSummaryAboveThreshold(t *testing.T) {
	m := &Model{
		streaming:   true,
		streamStart: time.Now().Add(-3 * time.Second),
		verbPast:    "Cogitated",
	}
	_, _ = applyDone(m, doneMsg{output: "hi"})

	if len(m.messages) < 2 {
		t.Fatalf("expected >=2 messages; got %d: %+v", len(m.messages), m.messages)
	}
	last := m.messages[len(m.messages)-1]
	prev := m.messages[len(m.messages)-2]

	if prev.Role != "assistant" || prev.Content != "hi" {
		t.Errorf("assistant must precede summary; got role=%q content=%q",
			prev.Role, prev.Content)
	}
	if last.Role != "thinking-summary" {
		t.Errorf("last message must be thinking-summary; got %q", last.Role)
	}
	if !strings.Contains(last.Content, "Cogitated") || !strings.Contains(last.Content, "for") {
		t.Errorf("summary missing verb/scaffolding; got %q", last.Content)
	}
}

// Short turn (< 2s) → no summary pushed. The threshold suppresses
// "Verbed for 0s" noise on quick answers.
func TestHandleDone_NoSummaryBelowThreshold(t *testing.T) {
	m := &Model{
		streaming:   true,
		streamStart: time.Now().Add(-500 * time.Millisecond),
		verbPast:    "Pondered",
	}
	_, _ = applyDone(m, doneMsg{output: "hi"})

	for _, msg := range m.messages {
		if msg.Role == "thinking-summary" {
			t.Errorf("unexpected summary under threshold: %+v", msg)
		}
	}
}

func TestHandleDone_QueuesFinalAnswerForScrollback(t *testing.T) {
	answer := "start\n" + strings.Repeat("middle\n", 40) + "end"
	m := &Model{
		streaming:   true,
		streamStart: time.Now(),
	}

	_, _ = applyDone(m, doneMsg{output: answer})

	if len(m.pendingScrollback) != 2 {
		t.Fatalf("final answer must be queued for scrollback, got %#v", m.pendingScrollback)
	}
	if !strings.Contains(m.pendingScrollback[0], "start") || !strings.Contains(m.pendingScrollback[0], "end") {
		t.Fatalf("scrollback must contain the full answer, got %q", m.pendingScrollback[0])
	}
	if !strings.Contains(m.pendingScrollback[1], "──") {
		t.Fatalf("completed turn should end with a divider, got %q", m.pendingScrollback[1])
	}
	if live := getLiveMessages(m); len(live) != 0 {
		t.Fatalf("completed answer should not stay clipped in live viewport: %#v", live)
	}
}

// Error path → assistant fallback + system error, but NO thinking-summary
// (already-noisy line gets gloating "Verbed for 3s" on top otherwise).
func TestHandleDone_ErrorPathSkipsSummary(t *testing.T) {
	m := &Model{
		streaming:   true,
		streamStart: time.Now().Add(-5 * time.Second),
		verbPast:    "Plotted",
	}
	_, _ = applyDone(m, doneMsg{err: errFixed("boom")})

	for _, msg := range m.messages {
		if msg.Role == "thinking-summary" {
			t.Errorf("summary must not appear on error: %+v", msg)
		}
	}
}

// Empty verbPast (e.g. summary called without a corresponding submit)
// → defensively skip the summary; rendering "for Ns" without a verb is
// worse than silence.
func TestHandleDone_SkipsSummaryWhenVerbPastMissing(t *testing.T) {
	m := &Model{
		streaming:   true,
		streamStart: time.Now().Add(-3 * time.Second),
		verbPast:    "",
	}
	_, _ = applyDone(m, doneMsg{output: "hi"})
	for _, msg := range m.messages {
		if msg.Role == "thinking-summary" {
			t.Errorf("must not push summary with empty verb: %+v", msg)
		}
	}
}

func TestRenderMessage_ThinkingSummary(t *testing.T) {
	m := &Model{}
	out := renderMessage(m, chatMessage{
		Role:    "thinking-summary",
		Content: "Cogitated for 6s",
	})
	for _, want := range []string{"✻", "Cogitated for 6s"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary rendering missing %q; got %q", want, out)
		}
	}
}
