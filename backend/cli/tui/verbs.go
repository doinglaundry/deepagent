package tui

import "math/rand"

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
