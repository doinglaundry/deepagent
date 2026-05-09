package tui

import (
	"strings"
	"testing"
	"time"
)

// renderBanner must include the SGADK figlet art (5 rows), the
// subtitle, version, and author. These are the visual identity tokens
// the user sees on every startup; if any drops out the banner stops
// matching the spec.
func TestRenderBanner_ContainsIdentityTokens(t *testing.T) {
	got := renderBanner()

	// All 5 figlet rows of bannerASCII must survive into the output.
	for _, row := range strings.Split(bannerASCII, "\n") {
		if !strings.Contains(got, row) {
			t.Errorf("banner missing figlet row %q", row)
		}
	}
	for _, want := range []string{bannerSubtitle, bannerVersion, bannerAuthor} {
		if !strings.Contains(got, want) {
			t.Errorf("banner missing token %q", want)
		}
	}
}

// freshMessages always returns exactly one entry, role="banner",
// non-empty content. Both startup and /clear depend on this shape.
func TestFreshMessages_SingleBannerEntry(t *testing.T) {
	got := freshMessages()
	if len(got) != 1 {
		t.Fatalf("freshMessages: len = %d, want 1", len(got))
	}
	if got[0].Role != "banner" {
		t.Errorf("freshMessages[0].Role = %q, want %q", got[0].Role, "banner")
	}
	if strings.TrimSpace(got[0].Content) == "" {
		t.Errorf("freshMessages[0].Content is blank")
	}
}

// renderMessage on a banner entry must echo Content verbatim — no
// prefix, no markdown rendering — because the content is already
// pre-rendered ANSI.
func TestRenderMessage_BannerVerbatim(t *testing.T) {
	m := &Model{}
	body := renderBanner()
	got := m.renderMessage(chatMessage{Role: "banner", Content: body})
	if got != body {
		t.Errorf("renderMessage(banner) altered the content:\nwant: %q\n got: %q", body, got)
	}
}

// weekdayPalette must cover all 7 weekdays with distinct, non-empty
// colour codes. A sparse / duplicated palette would silently demote
// the rotation to "same colour every day".
func TestWeekdayPalette_AllSevenDaysDistinct(t *testing.T) {
	if len(weekdayPalette) != 7 {
		t.Fatalf("len(weekdayPalette) = %d, want 7", len(weekdayPalette))
	}
	seen := make(map[string]time.Weekday, 7)
	for d := time.Sunday; d <= time.Saturday; d++ {
		c := string(weekdayPalette[d])
		if c == "" {
			t.Errorf("weekdayPalette[%s] is empty", d)
		}
		if prev, dup := seen[c]; dup {
			t.Errorf("weekdayPalette[%s] = %q duplicates [%s]", d, c, prev)
		}
		seen[c] = d
	}
}

// bannerArtStyle picks the colour for the *current* weekday. We
// can't pin "today" deterministically without injecting a clock, but
// we can at least assert the picked colour is one of the seven
// declared palette entries.
func TestBannerArtStyle_UsesPaletteEntry(t *testing.T) {
	got := bannerArtStyle().GetForeground()
	for _, want := range weekdayPalette {
		if got == want {
			return
		}
	}
	t.Errorf("bannerArtStyle foreground %v not found in weekdayPalette", got)
}
