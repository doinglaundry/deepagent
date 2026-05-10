package tui

import (
	"strings"
	"testing"
	"time"
)

// renderBanner must echo all visual identity tokens (art rows + subtitle/version/author).
func TestRenderBanner_ContainsIdentityTokens(t *testing.T) {
	got := renderBanner()

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

// freshMessages always returns one non-empty banner-role entry.
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

// renderMessage echoes pre-rendered ANSI banner content verbatim (no markdown).
func TestRenderMessage_BannerVerbatim(t *testing.T) {
	m := &Model{}
	body := renderBanner()
	got := m.renderMessage(chatMessage{Role: "banner", Content: body})
	if got != body {
		t.Errorf("renderMessage(banner) altered the content:\nwant: %q\n got: %q", body, got)
	}
}

// weekdayPalette must have 7 distinct, non-empty colour codes.
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

// bannerArtStyle's chosen colour must be one of the declared palette entries.
func TestBannerArtStyle_UsesPaletteEntry(t *testing.T) {
	got := bannerArtStyle().GetForeground()
	for _, want := range weekdayPalette {
		if got == want {
			return
		}
	}
	t.Errorf("bannerArtStyle foreground %v not found in weekdayPalette", got)
}
