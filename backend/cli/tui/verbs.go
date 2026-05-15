package tui

import (
	"math/rand"

	"github.com/charmbracelet/lipgloss"
)

// shimmerWidth is the highlight-window width in bytes. 3 matches helixent's
// SHIMMER_WIDTH; tighter feels staccato, wider feels like a full re-render.
const shimmerWidth = 3

// verbs holds (present, past) pairs used by the thinking indicator.
// The present-tense form (with "…") drives the live readout while the
// model is streaming; the past-tense form lands in scrollback as a
// completion summary. Indexes must stay aligned so a turn that starts
// "Moonwalking…" finishes "Moonwalked for Ns".
//
// Keep the pool short and the verbs concrete — abstract words ("Thinking",
// "Processing") read as filler.
var verbs = []struct{ Present, Past string }{
	{"Moonwalking", "Moonwalked"},
	{"Cogitating", "Cogitated"},
	{"Pondering", "Pondered"},
	{"Brewing", "Brewed"},
	{"Marinating", "Marinated"},
	{"Tinkering", "Tinkered"},
	{"Conjuring", "Conjured"},
	{"Distilling", "Distilled"},
	{"Stewing", "Stewed"},
	{"Noodling", "Noodled"},
	{"Mulling", "Mulled"},
	{"Percolating", "Percolated"},
	{"Plotting", "Plotted"},
	{"Reasoning", "Reasoned"},
	{"Scheming", "Schemed"},
}

// pickVerb returns a random (present, past) pair. Go 1.20+ seeds the
// global rand automatically, so no manual rand.Seed call is needed.
func pickVerb() (present, past string) {
	v := verbs[rand.Intn(len(verbs))]
	return v.Present, v.Past
}

// renderShimmer overlays a shimmerWidth-wide highlight window onto text.
// offset advances per tick; the visible window wraps modulo
// (len(text)+2*shimmerWidth) so it scans in from the left and out to
// the right, leaving a base-styled frame at the cycle endpoints. Byte
// slicing is fine because the verb pool is ASCII; future CJK verbs
// would need a rune-aware variant but that's a non-existent need today
// (AGENTS.md "矫枉过正预警").
func renderShimmer(text string, offset int, base, bright lipgloss.Style) string {
	if text == "" {
		return ""
	}
	cycle := len(text) + shimmerWidth*2
	pos := offset % cycle
	start := pos - shimmerWidth
	end := pos + shimmerWidth
	if start >= len(text) || end <= 0 {
		return base.Render(text)
	}
	lo := start
	if lo < 0 {
		lo = 0
	}
	hi := end
	if hi > len(text) {
		hi = len(text)
	}
	return base.Render(text[:lo]) + bright.Render(text[lo:hi]) + base.Render(text[hi:])
}
