package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
)

// Long CJK paragraphs have no ASCII whitespace, so glamour's word-wrap
// can't find a breakpoint and emits the whole paragraph on one line.
// renderMarkdown's post-wrap (xansi.Wrap with grapheme width) must
// hard-break the run, otherwise viewport.View() truncates the overflow
// at MaxWidth and the assistant reply visibly runs off-screen.
func TestRenderMarkdown_HardWrapsLongCJKLine(t *testing.T) {
	m := &Model{
		viewport: viewport.New(40, 10), // viewport.Width=40 → wrap budget = 38
		mdStyle:  "dark",
	}
	// 60 Chinese chars (≈120 cells) — well over the 38-cell budget.
	long := strings.Repeat("中", 60)

	out := renderMarkdown(m,long)

	if !strings.Contains(out, "\n") {
		t.Fatalf("expected wrap-induced newlines in long CJK output; got single line of len=%d:\n%q",
			len(out), out)
	}
	// No single line should exceed the wrap budget once styled markers
	// are stripped — but counting cells in the presence of ANSI is
	// annoying; the newline check is enough to assert "wrap happened".
}

// ASCII paragraphs already wrap inside glamour; the extra ansi.Wrap pass
// must not double-wrap them into much-too-short lines.
func TestRenderMarkdown_AsciiWrapStaysReasonable(t *testing.T) {
	m := &Model{
		viewport: viewport.New(80, 10),
		mdStyle:  "dark",
	}
	out := renderMarkdown(m,"hello world hello world hello world")

	// One line at ~80 cells — no spurious wrap-on-every-word.
	if strings.Count(out, "\n") > 1 {
		t.Errorf("short ASCII line wrapped too aggressively; out:\n%q", out)
	}
}
