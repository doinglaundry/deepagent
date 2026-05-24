package tui

import (
	"strings"
	"testing"
)

// Assistant continuation lines must be indented 2 cells so they align
// under the "⏺ " prefix. Without indentation, wrapped/multi-line replies
// visually break out of the message block on the second line onward.
func TestRenderMessage_AssistantContinuationIndent(t *testing.T) {
	m := &Model{}
	// Use Content (not Rendered) so we test the indent logic directly
	// without glamour interposing — the raw body still flows through
	// the same continuation-indent replacement path.
	out := renderMessage(m,chatMessage{
		Role:    "assistant",
		Content: "first line\nsecond line\nthird line",
	})

	if !strings.Contains(out, "\n  second line") {
		t.Errorf("second line must be indented by 2 cells; got:\n%q", out)
	}
	if !strings.Contains(out, "\n  third line") {
		t.Errorf("third line must be indented by 2 cells; got:\n%q", out)
	}
	if strings.Contains(out, "\nsecond line") {
		t.Errorf("continuation line found without indent (regression); got:\n%q", out)
	}
}

// User messages must NOT get the assistant-style 2-cell continuation
// indent — only assistant replies do, because only they own a multi-cell
// prefix glyph to align under. The user echo block has its own 1-cell
// background padding for the card "shadow" look, which is fine.
func TestRenderMessage_UserContinuationNotIndented(t *testing.T) {
	m := &Model{}
	out := renderMessage(m,chatMessage{
		Role:    "user",
		Content: "first line\nsecond line",
	})
	if strings.Contains(out, "\n  second line") {
		t.Errorf("user continuation must not borrow the assistant indent; got:\n%q", out)
	}
}
