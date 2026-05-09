package tui

import (
	"strings"
	"testing"
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
