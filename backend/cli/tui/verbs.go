package tui

import (
	"math/rand"

	"github.com/charmbracelet/lipgloss"
)

const shimmerWidth = 3

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

func pickVerb() (present, past string) {
	v := verbs[rand.Intn(len(verbs))]
	return v.Present, v.Past
}

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
