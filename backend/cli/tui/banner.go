package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

// bannerVersion is shown inside the boxed welcome card's top border AND
// in the compact fallback's right column. Bump it in the same commit you
// prepend a banner_notes.go entry — drift between the two is a code
// review red flag (see yaml/CHANGELOG.md style of pairing version + diff).
const bannerVersion = "1.2.0"

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

// renderBanner picks the wide boxed welcome card or a vertical compact
// fallback based on terminal width. Width 0 (the WindowSizeMsg-not-yet-
// fired first paint) goes to compact; once the resize message arrives,
// rebuildHistory re-renders the banner row with the real width and the
// boxed form takes over — visible as a brief one-frame swap on startup.
func renderBanner(width int, modelName, cwd string) string {
	if width >= bannerMinWidth {
		return renderWelcomeCard(width, modelName, cwd)
	}
	return renderBannerCompact(modelName, cwd)
}

// renderBannerCompact lays the ASCII glyph on the left with version /
// model / cwd stacked on the right, separated by a fixed 5-space gutter.
// Each glyph row is pre-padded to maxGlyph width so the right column
// always starts at the same column regardless of figlet row variance.
func renderBannerCompact(modelName, cwd string) string {
	art := bannerArtStyle()
	glyph := strings.Split(bannerASCII, "\n")

	maxGlyph := 0
	for _, line := range glyph {
		if w := lipgloss.Width(line); w > maxGlyph {
			maxGlyph = w
		}
	}

	info := []string{
		"eino-cli v" + bannerVersion,
		"",
		modelName,
		cwd,
		"",
	}

	rows := make([]string, len(glyph))
	for i, line := range glyph {
		padded := lipgloss.PlaceHorizontal(maxGlyph, lipgloss.Left, art.Render(line))
		right := ""
		if i < len(info) && info[i] != "" {
			right = dimStyle.Render(info[i])
		}
		rows[i] = padded + "     " + right
	}
	return strings.Join(rows, "\n")
}

// freshMessages returns the seed messages for a new or /clear'd session.
// width can be 0 at first paint (WindowSizeMsg pending) — the banner
// renderer handles that by falling back to compact form.
func freshMessages(width int, modelName, cwd string) []chatMessage {
	return []chatMessage{{Role: "banner", Content: renderBanner(width, modelName, cwd)}}
}

// boxTitleLine renders the top border of the welcome card with an inline
// title embedded after a 3-dash run, e.g.
//
//	╭─── eino-cli v1.1.0 ─────────────────╮
//
// Output is always exactly `width` display cells. Title is truncated with
// "…" if it can't fit so the corners stay aligned.
//
// lipgloss has no border-with-title primitive — hand-stitching the top
// line and letting lipgloss draw the rest is the cheapest fix until that
// API lands upstream.
func boxTitleLine(width int, title string) string {
	const leftPad = 3
	inner := width - 2
	if inner < leftPad+3 {
		return "╭" + strings.Repeat("─", inner) + "╮"
	}
	label := " " + title + " "
	labelW := runewidth.StringWidth(label)
	if leftPad+labelW > inner {
		budget := inner - leftPad - 2
		if budget < 1 {
			budget = 1
		}
		label = " " + runewidth.Truncate(title, budget, "…") + " "
		labelW = runewidth.StringWidth(label)
	}
	return "╭" + strings.Repeat("─", leftPad) + label + strings.Repeat("─", inner-leftPad-labelW) + "╮"
}

// splitColumns weaves two column slices into row strings shaped like
//
//	│ <left, padded to leftW> │ <right, padded to rightW> │
//
// Each output row is exactly leftW + rightW + 7 cells (two side borders +
// one middle border + four padding spaces). Shorter column gets blank-
// padded rows; output length == max(len(left), len(right)).
func splitColumns(left, right []string, leftW, rightW int) []string {
	n := len(left)
	if len(right) > n {
		n = len(right)
	}
	out := make([]string, n)
	for i := 0; i < n; i++ {
		out[i] = "│ " + getRow(left, i, leftW) + " │ " + getRow(right, i, rightW) + " │"
	}
	return out
}

// getRow returns the i-th element of rows padded or truncated to exactly
// `width` display cells. Past-the-end indices yield a blank-padded line.
//
// Uses lipgloss.Width (not raw rune count) so ANSI-styled rows that come
// pre-padded from lipgloss.PlaceHorizontal pass through unchanged.
func getRow(rows []string, i, width int) string {
	s := ""
	if i < len(rows) {
		s = rows[i]
	}
	w := lipgloss.Width(s)
	switch {
	case w == width:
		return s
	case w < width:
		return s + strings.Repeat(" ", width-w)
	default:
		return runewidth.Truncate(s, width, "…")
	}
}

const (
	bannerMinWidth = 80
	// bannerMaxWidth caps the card so it stops growing past the point where
	// content has anything to fill the right column with. Calibrated to
	// 7 (chrome) + bannerLeftMax (48) + 65 (= "• " + the longest release
	// note that fits on one line) = 120. Past this, the card just sits
	// left-aligned in the wider terminal — empty trailing right-column
	// space is the symptom we're cutting (see #review-banner-at-160w).
	bannerMaxWidth = 120
	bannerLeftMin  = 32
	bannerLeftMax  = 48
	bannerMaxNotes = 5
)

// renderWelcomeCard renders the boxed two-column welcome card for a wide
// terminal (width >= bannerMinWidth). Layout:
//
//	╭─── eino-cli v1.1.0 ──── … ───╮
//	│ <welcome + glyph + ident> │ <what's new + notes> │
//	...
//	╰── … ──╯
//
// Caller is responsible for the width gate; this function will floor to
// bannerMinWidth so the helpers never get into a negative-padding spiral
// when called from a test that forgets to clamp.
func renderWelcomeCard(width int, modelName, cwd string) string {
	switch {
	case width < bannerMinWidth:
		width = bannerMinWidth
	case width > bannerMaxWidth:
		width = bannerMaxWidth
	}
	leftW, rightW := chooseColumnWidths(width)

	leftCol := renderLeftColumn(leftW, modelName, cwd)
	rightCol := renderRightColumn(rightW)
	rows := splitColumns(leftCol, rightCol, leftW, rightW)

	top := boxTitleLine(width, "eino-cli v"+bannerVersion)
	bottom := "╰" + strings.Repeat("─", width-2) + "╯"

	block := make([]string, 0, len(rows)+2)
	block = append(block, top)
	block = append(block, rows...)
	block = append(block, bottom)
	return colorizeBorders(strings.Join(block, "\n"), bannerArtStyle())
}

// chooseColumnWidths splits the box's content area into left + right
// columns. Algorithm: aim for 50/50 of (width-7), then clamp left into
// [bannerLeftMin, bannerLeftMax] so the glyph fits on one side and notes
// have room to wrap on the other.
//
// The "-7" budget is the structural cost of three "│" chars plus four
// padding spaces per row — see splitColumns for the shape.
func chooseColumnWidths(width int) (leftW, rightW int) {
	leftW = (width - 7) / 2
	switch {
	case leftW < bannerLeftMin:
		leftW = bannerLeftMin
	case leftW > bannerLeftMax:
		leftW = bannerLeftMax
	}
	rightW = width - 7 - leftW
	if rightW < 10 {
		rightW = 10
	}
	return
}

// renderLeftColumn fills the left side: Welcome / weekday-coloured glyph /
// model + cwd. Rows are pre-padded to leftW via lipgloss.PlaceHorizontal
// so they pass through getRow untouched (ANSI escapes are not raw width).
func renderLeftColumn(leftW int, modelName, cwd string) []string {
	art := bannerArtStyle()
	glyph := strings.Split(bannerASCII, "\n")
	glyphFits := true
	for _, line := range glyph {
		if lipgloss.Width(line) > leftW-2 {
			glyphFits = false
			break
		}
	}

	rows := []string{
		"",
		lipgloss.PlaceHorizontal(leftW, lipgloss.Center, "Welcome back!"),
		"",
	}
	if glyphFits {
		for _, line := range glyph {
			rows = append(rows, lipgloss.PlaceHorizontal(leftW, lipgloss.Center, art.Render(line)))
		}
		rows = append(rows, "")
	}
	rows = append(rows,
		lipgloss.PlaceHorizontal(leftW, lipgloss.Left, "   "+dimStyle.Render(modelName)),
		lipgloss.PlaceHorizontal(leftW, lipgloss.Left, "   "+dimStyle.Render(cwd)),
	)
	return rows
}

// renderRightColumn fills the right side: "What's new" header plus up to
// bannerMaxNotes wrapped bullet lines. Each note word-wraps to rightW-2
// (the bullet/continuation prefix eats 2 cells).
func renderRightColumn(rightW int) []string {
	rows := []string{
		headerTitleStyle.Render("What's new"),
		"",
	}
	notes := releaseNotes
	if len(notes) > bannerMaxNotes {
		notes = notes[:bannerMaxNotes]
	}
	for _, note := range notes {
		wrapped := wordWrap(note, rightW-2)
		for j, line := range wrapped {
			prefix := "• "
			if j > 0 {
				prefix = "  "
			}
			rows = append(rows, prefix+line)
		}
	}
	return rows
}

// wordWrap greedily packs words into lines of at most `width` cells.
// A single word longer than width gets emitted on its own and overflows;
// callers downstream (getRow) will truncate visually.
func wordWrap(s string, width int) []string {
	if width < 1 {
		return []string{s}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	out := []string{}
	cur := words[0]
	for _, w := range words[1:] {
		if lipgloss.Width(cur+" "+w) <= width {
			cur += " " + w
			continue
		}
		out = append(out, cur)
		cur = w
	}
	return append(out, cur)
}

// boxBorderChars enumerates every box-drawing rune we emit in the welcome
// card. Pulled into a var so colorizeBorders can blanket-recolor them.
var boxBorderChars = []string{"╭", "╮", "╰", "╯", "│", "─"}

// colorizeBorders wraps every box-drawing rune in `text` with `style`'s
// ANSI, leaving the box's interior content (welcome / glyph / notes,
// which already carry their own styles) alone. This is how the weekday
// palette gets applied "only to the border" per spec §0.
//
// Each rune ends up as its own escape-sequence pair (verbose on the
// wire, ~7x byte-blow-up on the top line) but visually correct and
// trivial to reason about.
func colorizeBorders(text string, style lipgloss.Style) string {
	args := make([]string, 0, 2*len(boxBorderChars))
	for _, c := range boxBorderChars {
		args = append(args, c, style.Render(c))
	}
	return strings.NewReplacer(args...).Replace(text)
}
