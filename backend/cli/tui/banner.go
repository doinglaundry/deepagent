package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// Banner identity shown at session start and after /clear (cosmetic only).
const (
	bannerSubtitle = "AI-Driven Development Kit"
	bannerVersion  = "1.0.1"
	bannerAuthor   = "YINGBAO SUN"
)

// bannerASCII is "SGADK" in figlet "standard" font; backtick-literal preserves
// figlet's trailing-space spacing columns.
const bannerASCII = ` ____   ____    _    ____  _  __
/ ___| / ___|  / \  |  _ \| |/ /
\___ \| |  _  / _ \ | | | | ' / 
 ___) | |_| |/ ___ \| |_| | . \ 
|____/ \____/_/   \_\____/|_|\_\`

// weekdayPalette: ROYGBIV rotation indexed by time.Weekday() (Sun=0 … Sat=6).
var weekdayPalette = [7]lipgloss.Color{
	time.Sunday:    "196", // red
	time.Monday:    "208", // orange
	time.Tuesday:   "220", // gold
	time.Wednesday: "46",  // green
	time.Thursday:  "51",  // cyan
	time.Friday:    "33",  // blue
	time.Saturday:  "129", // purple
}

// bannerArtStyle picks the day's palette colour; re-evaluated each call so
// a session crossing midnight picks up the new colour after /clear.
func bannerArtStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(weekdayPalette[time.Now().Weekday()])
}

// renderBanner returns the welcome banner block (art + subtitle/version/author).
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

// freshMessages returns the seed messages for a new or /clear'd session.
func freshMessages() []chatMessage {
	return []chatMessage{{Role: "banner", Content: renderBanner()}}
}
