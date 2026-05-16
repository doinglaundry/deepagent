package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/viewport"
)

// Few-line content → viewport must shrink down to that line count so the
// input box hugs the message block, instead of being padded to the screen
// bottom by viewport.View()'s default blank-line fill.
func TestRecomputeLayout_ShrinksViewportToContent(t *testing.T) {
	m := &Model{
		width:    80,
		height:   30, // generous screen, content is the constraint
		viewport: viewport.New(80, 25),
	}
	m.viewport.SetContent("only a few\nshort lines\nhere")

	m.recomputeLayout()

	if m.viewport.Height >= 25 {
		t.Errorf("viewport must shrink to fit content; got Height=%d (want < 25)",
			m.viewport.Height)
	}
	if m.viewport.Height < 3 {
		t.Errorf("viewport height collapsed below the 1-line floor; got %d",
			m.viewport.Height)
	}
}

// Overflowing content → viewport must clamp at the chrome-aware max so
// the input + footer still have room. The total line count we feed in
// (200) is well above any plausible max for a 30-row screen.
func TestRecomputeLayout_ClampsViewportAtBudget(t *testing.T) {
	m := &Model{
		width:    80,
		height:   30,
		viewport: viewport.New(80, 25),
	}
	// 200 short lines — exceeds the budget for any plausible chrome.
	m.viewport.SetContent(strings.Repeat("filler line\n", 200))

	m.recomputeLayout()

	// Chrome = stream(0) + todo(0) + input(3) + footer(1) = 4.
	// So vpMax = 30 - 4 = 26.
	const wantMax = 26
	if m.viewport.Height != wantMax {
		t.Errorf("viewport must clamp at vpMax=%d; got Height=%d",
			wantMax, m.viewport.Height)
	}
}

// Empty content (e.g. between /clear and rebuildHistory) → height must
// land at the 1-line floor so the input doesn't visually float at the
// top of the screen with no contextual anchor.
func TestRecomputeLayout_EmptyContentFloorsAtOne(t *testing.T) {
	m := &Model{
		width:    80,
		height:   30,
		viewport: viewport.New(80, 10),
	}
	m.viewport.SetContent("")

	m.recomputeLayout()

	if m.viewport.Height < 1 {
		t.Errorf("viewport height must be >= 1 on empty content; got %d",
			m.viewport.Height)
	}
}

// rebuildHistory must propagate to viewport.Height — otherwise pushing a
// message would leave the viewport stuck at the previously-computed size.
func TestRebuildHistory_SyncsViewportHeight(t *testing.T) {
	m := &Model{
		width:    80,
		height:   30,
		viewport: viewport.New(80, 25),
	}

	// Seed with a banner-style message (uses Content verbatim, no markdown).
	m.messages = []chatMessage{
		{Role: "system", Content: "line1\nline2\nline3"},
	}
	m.rebuildHistory()
	first := m.viewport.Height

	m.messages = append(m.messages,
		chatMessage{Role: "system", Content: strings.Repeat("x\n", 50)},
	)
	m.rebuildHistory()

	if m.viewport.Height <= first {
		t.Errorf("viewport must grow as content grows; first=%d after=%d",
			first, m.viewport.Height)
	}
}
