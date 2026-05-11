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
