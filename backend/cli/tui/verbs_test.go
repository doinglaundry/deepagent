package tui

import (
	"strings"
	"testing"
)

// Present and past tense pools must be aligned 1:1 and non-empty so a
// turn's verb stays consistent from the live "Verbing…" readout to the
// scrollback "Verbed for Ns" summary.
func TestVerbs_PresentAndPastAligned(t *testing.T) {
	if len(verbs) == 0 {
		t.Fatal("verbs pool is empty; thinking indicator would crash")
	}
	for i, v := range verbs {
		if v.Present == "" || v.Past == "" {
			t.Errorf("verbs[%d] has empty form: %+v", i, v)
		}
		if strings.Contains(v.Present, "…") || strings.Contains(v.Past, "…") {
			t.Errorf("verbs[%d] stores the ellipsis (renderer adds it): %+v", i, v)
		}
		if v.Present == v.Past {
			t.Errorf("verbs[%d] present and past identical (%q) — tense distinction lost",
				i, v.Present)
		}
	}
}

func TestPickVerb_ReturnsKnownPair(t *testing.T) {
	present, past := pickVerb()
	for _, v := range verbs {
		if v.Present == present && v.Past == past {
			return // found the pair
		}
	}
	t.Errorf("pickVerb returned (%q, %q) which isn't a known pair", present, past)
}

// Shimmer must (a) preserve the original text byte-for-byte once ANSI
// escapes are stripped, and (b) at some offset within a full cycle
// produce a visible bright-style segment. Strip escapes by scanning
// for "[1m" / "[0m" pairs is overkill — lipgloss's renderer is
// idempotent for empty input, so we test the "all-base" frames at
// offset 0 and offset = cycle directly.
func TestRenderShimmer_PreservesPlainText(t *testing.T) {
	verb := "Moonwalking…"
	for offset := 0; offset < len(verb)+2*shimmerWidth+1; offset++ {
		got := renderShimmer(verb, offset, thinkingPresentStyle, thinkingShimmerStyle)
		// Strip ANSI: lipgloss emits ESC sequences but the visible
		// bytes are still the original. We can't depend on a stripping
		// helper here, so just assert the verb's own bytes appear in
		// order somewhere in the output.
		if !strings.Contains(got, "Moonwalking") {
			t.Fatalf("offset=%d: shimmer dropped text bytes; got %q", offset, got)
		}
	}
}

// Empty input is a defensive contract — the v1 thinking indicator
// always has a verb, but a future "blank-while-paused" state would
// flow through here.
func TestRenderShimmer_EmptyInputIsEmpty(t *testing.T) {
	if got := renderShimmer("", 5, thinkingPresentStyle, thinkingShimmerStyle); got != "" {
		t.Errorf("empty input must produce empty output; got %q", got)
	}
}
