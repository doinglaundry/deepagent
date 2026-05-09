package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Banner identity shown at session start (and after /clear). Edit
// these constants when bumping the version or transferring ownership;
// the rendered banner is purely cosmetic so even mismatches with the
// real build won't break anything.
const (
	bannerSubtitle = "AI-Driven Development Kit"
	bannerVersion  = "1.0.1"
	bannerAuthor   = "YINGBAO SUN"
)

// bannerASCII is "SGADK" rendered in figlet's "standard" font (5
// rows). Kept as a raw string literal so the trailing-space columns
// that figlet emits to keep adjacent letters from butting up against
// each other survive intact (gofmt won't trim trailing whitespace
// inside backtick strings).
const bannerASCII = ` ____   ____    _    ____  _  __
/ ___| / ___|  / \  |  _ \| |/ /
\___ \| |  _  / _ \ | | | | ' / 
 ___) | |_| |/ ___ \| |_| | . \ 
|____/ \____/_/   \_\____/|_|\_\`

// weekdayPalette is a 7-colour ROYGBIV rotation indexed by
// time.Weekday() (Sunday=0 … Saturday=6). 256-colour terminal codes
// chosen for both pleasant saturation and clear distinguishability —
// turning "what day is it?" into a tiny guessing game every session.
var weekdayPalette = [7]lipgloss.Color{
	time.Sunday:    "196", // red
	time.Monday:    "208", // orange
	time.Tuesday:   "220", // gold
	time.Wednesday: "46",  // green
	time.Thursday:  "51",  // cyan
	time.Friday:    "33",  // blue
	time.Saturday:  "129", // purple
}

// bannerArtStyle is the lipgloss style applied to the SGADK block
// letters. Re-evaluated on every call so a long-running session that
// crosses midnight picks up the new day's colour after /clear (the
// only path that re-runs renderBanner).
func bannerArtStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(weekdayPalette[time.Now().Weekday()])
}

// renderBanner returns the welcome banner — figlet block letters
// stacked over the subtitle / version / author lines, ready to be
// pushed as a single chatMessage of role "banner". Called from New()
// at startup and from /clear, so the user sees the same identity
// prompt at the top of every fresh session.
func renderBanner() string {
	parts := []string{
		bannerArtStyle().Render(bannerASCII),
		"",
		dimStyle.Render(bannerSubtitle),
		dimStyle.Render("Version: " + bannerVersion),
		dimStyle.Render("Author:  " + bannerAuthor),
	}
	return strings.Join(parts, "\n")
}

// freshMessages returns the message slice a brand-new (or freshly
// /clear'd) session should start with. Currently just the welcome
// banner, kept in a single helper so New() and /clear can share the
// definition and never drift apart.
func freshMessages() []chatMessage {
	return []chatMessage{{Role: "banner", Content: renderBanner()}}
}
