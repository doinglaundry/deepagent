package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// renderBanner must echo every identity token regardless of dispatch path:
// figlet glyph rows are present (compact path keeps them), and the version
// string appears (boxed path: in the top border; compact path: stacked
// next to the glyph).
func TestRenderBanner_ContainsIdentityTokens(t *testing.T) {
	got := renderBanner(0, "test-model", "/tmp/test")
	for _, row := range strings.Split(bannerASCII, "\n") {
		if !strings.Contains(got, row) {
			t.Errorf("banner missing figlet row %q", row)
		}
	}
	if !strings.Contains(got, bannerVersion) {
		t.Errorf("banner missing version %q", bannerVersion)
	}
}

// freshMessages always returns one non-empty banner-role entry.
func TestFreshMessages_SingleBannerEntry(t *testing.T) {
	got := freshMessages(0, "test-model", "/tmp/test")
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
	body := renderBanner(120, "test-model", "/tmp/test")
	got := renderMessage(m, chatMessage{Role: "banner", Content: body})
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

// Wide-terminal path produces a box: ╭ on the first line, ╰ on the last,
// every line exactly `width` cells. The cell-width invariant is what
// catches off-by-one regressions in cardColumnWidths or buildCardRows —
// rendering looks "fine" until one row drifts and the whole frame
// shears at the next terminal repaint.
func TestRenderWelcomeCard_BoxedAtWideWidth(t *testing.T) {
	const width = 120
	got := renderWelcomeCard(width, "moonshot-v1-auto", "/Users/bytedance/go/src/content/LLM")
	lines := strings.Split(got, "\n")
	if len(lines) < 8 {
		t.Fatalf("boxed card too short: %d lines", len(lines))
	}
	if !strings.Contains(lines[0], "╭") || !strings.Contains(lines[0], "eino-cli v"+bannerVersion) {
		t.Errorf("top line missing corner or version title: %q", lines[0])
	}
	if !strings.Contains(lines[len(lines)-1], "╰") {
		t.Errorf("bottom line missing corner: %q", lines[len(lines)-1])
	}
	for i, line := range lines {
		if w := lipgloss.Width(line); w != width {
			t.Errorf("line %d width = %d, want %d: %q", i, w, width, line)
		}
	}
}

// Terminals wider than bannerMaxWidth must NOT stretch the card to fill
// — past the natural content width the right column just bleeds empty
// space. Cap at bannerMaxWidth and leave the rest of the terminal blank.
func TestRenderWelcomeCard_ClampsAboveMaxWidth(t *testing.T) {
	got := renderWelcomeCard(200, "kimi", "/tmp")
	lines := strings.Split(got, "\n")
	for i, line := range lines {
		if w := lipgloss.Width(line); w != bannerMaxWidth {
			t.Errorf("line %d width = %d, want %d (clamp): %q", i, w, bannerMaxWidth, line)
		}
	}
}

// Narrow-terminal path falls back to vertical glyph + info layout — no
// box-drawing corners — so we can spot accidental "boxed-everywhere"
// regressions.
func TestRenderBanner_CompactBelow80(t *testing.T) {
	got := renderBanner(70, "moonshot-v1-auto", "/tmp/short")
	if strings.Contains(got, "╭") || strings.Contains(got, "╰") {
		t.Errorf("width<80 must NOT render the boxed card; got:\n%s", got)
	}
	if !strings.Contains(got, "eino-cli v"+bannerVersion) {
		t.Errorf("compact form missing version: %s", got)
	}
	if !strings.Contains(got, "moonshot-v1-auto") {
		t.Errorf("compact form missing model name: %s", got)
	}
}

// releaseNotes drives the "What's new" column; an empty or blank first
// entry would render an orphan "•" with no text — fail loud when someone
// bumps bannerVersion without writing a note.
func TestReleaseNotes_NewestFirstNonEmpty(t *testing.T) {
	if len(releaseNotes) == 0 {
		t.Fatalf("releaseNotes is empty")
	}
	if strings.TrimSpace(releaseNotes[0]) == "" {
		t.Errorf("releaseNotes[0] (newest) is blank")
	}
}

// SIGWINCH from a narrow startup width to a wide one MUST re-bake the
// banner row's pre-rendered content. Otherwise a session that started
// at width 0 (no WindowSizeMsg yet) would stay on the compact fallback
// forever even after the user maximises the terminal — see handleResize.
func TestHandleResize_RebakesBannerForNewWidth(t *testing.T) {
	m := &Model{
		width:     0,
		height:    24,
		modelName: "moonshot-v1-auto",
		cwd:       "/Users/bytedance",
		viewport:  viewport.New(80, 10),
		messages:  freshMessages(0, "moonshot-v1-auto", "/Users/bytedance"),
	}
	if strings.Contains(m.messages[0].Content, "╭") {
		t.Fatalf("setup: width-0 banner should be compact (no ╭), got:\n%s", m.messages[0].Content)
	}

	_, _ = applyResize(m, tea.WindowSizeMsg{Width: 120, Height: 30})

	if !strings.Contains(m.messages[0].Content, "╭") {
		t.Errorf("after resize to width=120, banner should be boxed; got:\n%s", m.messages[0].Content)
	}
}
