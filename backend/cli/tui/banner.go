package tui

import (
	"strings"

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

// bannerArtStyle is the colour applied to the SGADK block letters.
// Bold magenta matches headerTitleStyle so the banner reads as part
// of the same visual identity as the header.
var bannerArtStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))

// renderBanner returns the welcome banner — figlet block letters
// stacked over the subtitle / version / author lines, ready to be
// pushed as a single chatMessage of role "banner". Called from New()
// at startup and from /clear, so the user sees the same identity
// prompt at the top of every fresh session.
func renderBanner() string {
	parts := []string{
		bannerArtStyle.Render(bannerASCII),
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
