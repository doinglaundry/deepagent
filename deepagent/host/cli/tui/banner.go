package tui

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

const (
	bannerVersion = "1.2.0" // Keep in sync with releaseNotes.
	bannerTitle   = "eino-cli v" + bannerVersion
	bannerASCII   = ` ____   ____    _    ____  _  __
/ ___| / ___|  / \  |  _ \| |/ /
\___ \| |  _  / _ \ | | | | ' / 
 ___) | |_| |/ ___ \| |_| | . \ 
|____/ \____/_/   \_\____/|_|\_\`

	bannerMinWidth       = 80
	bannerMaxWidth       = 120 // Wider cards only add empty space.
	bannerLeftMinWidth   = 32
	bannerLeftMaxWidth   = 48
	bannerRightMinWidth  = 10
	bannerMaxNotes       = 5
	bannerCardFrameWidth = 7
	bannerCompactGap     = "     "
)

var (
	weekdayPalette = [7]lipgloss.Color{
		time.Sunday:    "196", // red
		time.Monday:    "208", // orange
		time.Tuesday:   "220", // gold
		time.Wednesday: "46",  // green
		time.Thursday:  "51",  // cyan
		time.Friday:    "33",  // blue
		time.Saturday:  "129", // purple
	}

	cardBorderRunes = []string{"╭", "╮", "╰", "╯", "│", "─"}
)

func bannerArtStyle() lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(weekdayPalette[time.Now().Weekday()])
}

func renderBanner(width int, modelName, cwd string) string {
	if width >= bannerMinWidth {
		return renderWelcomeCard(width, modelName, cwd)
	}
	return renderBannerCompact(modelName, cwd)
}

func freshMessages(width int, modelName, cwd string) []chatMessage {
	return []chatMessage{{Role: "banner", Content: renderBanner(width, modelName, cwd)}}
}

func renderBannerCompact(modelName, cwd string) string {
	art := bannerArtStyle()
	glyphRows := strings.Split(bannerASCII, "\n")

	glyphWidth := 0
	for _, row := range glyphRows {
		if width := lipgloss.Width(row); width > glyphWidth {
			glyphWidth = width
		}
	}

	infoRows := []string{
		bannerTitle,
		"",
		modelName,
		cwd,
		"",
	}

	rows := make([]string, len(glyphRows))
	for i, row := range glyphRows {
		left := lipgloss.PlaceHorizontal(glyphWidth, lipgloss.Left, art.Render(row))
		right := ""
		if i < len(infoRows) && infoRows[i] != "" {
			right = dimStyle.Render(infoRows[i])
		}
		rows[i] = left + bannerCompactGap + right
	}
	return strings.Join(rows, "\n")
}

func renderWelcomeCard(width int, modelName, cwd string) string {
	width = clampBannerWidth(width)

	art := bannerArtStyle()
	leftWidth, rightWidth := cardColumnWidths(width)
	rows := buildCardRows(
		renderLeftColumn(leftWidth, modelName, cwd, art),
		renderRightColumn(rightWidth),
		leftWidth,
		rightWidth,
	)

	lines := make([]string, 0, len(rows)+2)
	lines = append(lines, boxTitleLine(width, bannerTitle))
	lines = append(lines, rows...)
	lines = append(lines, "╰"+strings.Repeat("─", width-2)+"╯")
	return styleCardBorders(strings.Join(lines, "\n"), art)
}

func clampBannerWidth(width int) int {
	switch {
	case width < bannerMinWidth:
		return bannerMinWidth
	case width > bannerMaxWidth:
		return bannerMaxWidth
	default:
		return width
	}
}

// boxTitleLine renders a fixed-width top border with an inline title.
func boxTitleLine(width int, title string) string {
	const titleInset = 3

	innerWidth := width - 2
	if innerWidth < titleInset+3 {
		return "╭" + strings.Repeat("─", innerWidth) + "╮"
	}

	label := " " + title + " "
	labelWidth := runewidth.StringWidth(label)
	if titleInset+labelWidth > innerWidth {
		budget := innerWidth - titleInset - 2
		if budget < 1 {
			budget = 1
		}
		label = " " + runewidth.Truncate(title, budget, "…") + " "
		labelWidth = runewidth.StringWidth(label)
	}

	return "╭" + strings.Repeat("─", titleInset) + label + strings.Repeat("─", innerWidth-titleInset-labelWidth) + "╮"
}

func cardColumnWidths(width int) (leftWidth, rightWidth int) {
	contentWidth := width - bannerCardFrameWidth
	leftWidth = contentWidth / 2
	switch {
	case leftWidth < bannerLeftMinWidth:
		leftWidth = bannerLeftMinWidth
	case leftWidth > bannerLeftMaxWidth:
		leftWidth = bannerLeftMaxWidth
	}

	rightWidth = contentWidth - leftWidth
	if rightWidth < bannerRightMinWidth {
		rightWidth = bannerRightMinWidth
	}
	return leftWidth, rightWidth
}

func buildCardRows(left, right []string, leftWidth, rightWidth int) []string {
	rowCount := len(left)
	if len(right) > rowCount {
		rowCount = len(right)
	}

	rows := make([]string, rowCount)
	for i := 0; i < rowCount; i++ {
		rows[i] = "│ " + fitCardRow(left, i, leftWidth) + " │ " + fitCardRow(right, i, rightWidth) + " │"
	}
	return rows
}

func fitCardRow(rows []string, idx, width int) string {
	row := ""
	if idx < len(rows) {
		row = rows[idx]
	}

	switch rowWidth := lipgloss.Width(row); {
	case rowWidth == width:
		return row
	case rowWidth < width:
		return row + strings.Repeat(" ", width-rowWidth)
	default:
		return runewidth.Truncate(row, width, "…")
	}
}

func renderLeftColumn(leftWidth int, modelName, cwd string, art lipgloss.Style) []string {
	glyphRows := strings.Split(bannerASCII, "\n")

	rows := []string{
		"",
		lipgloss.PlaceHorizontal(leftWidth, lipgloss.Center, "Welcome back!"),
		"",
	}

	if glyphFitsColumn(glyphRows, leftWidth) {
		for _, row := range glyphRows {
			rows = append(rows, lipgloss.PlaceHorizontal(leftWidth, lipgloss.Center, art.Render(row)))
		}
		rows = append(rows, "")
	}

	rows = append(rows,
		lipgloss.PlaceHorizontal(leftWidth, lipgloss.Left, "   "+dimStyle.Render(modelName)),
		lipgloss.PlaceHorizontal(leftWidth, lipgloss.Left, "   "+dimStyle.Render(cwd)),
	)
	return rows
}

func glyphFitsColumn(glyphRows []string, width int) bool {
	for _, row := range glyphRows {
		if lipgloss.Width(row) > width-2 {
			return false
		}
	}
	return true
}

func renderRightColumn(rightWidth int) []string {
	rows := []string{
		headerTitleStyle.Render("What's new"),
		"",
	}

	notes := releaseNotes
	if len(notes) > bannerMaxNotes {
		notes = notes[:bannerMaxNotes]
	}

	for _, note := range notes {
		for lineIndex, line := range wrapWords(note, rightWidth-2) {
			prefix := "• "
			if lineIndex > 0 {
				prefix = "  "
			}
			rows = append(rows, prefix+line)
		}
	}
	return rows
}

// wrapWords greedily fills lines up to width. Long single words overflow
// and are truncated later by fitCardRow.
func wrapWords(text string, width int) []string {
	if width < 1 {
		return []string{text}
	}

	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}

	lines := []string{words[0]}
	for _, word := range words[1:] {
		current := lines[len(lines)-1]
		if lipgloss.Width(current+" "+word) <= width {
			lines[len(lines)-1] = current + " " + word
			continue
		}
		lines = append(lines, word)
	}
	return lines
}

// styleCardBorders only recolors the box-drawing runes, not the content.
func styleCardBorders(text string, style lipgloss.Style) string {
	args := make([]string, 0, 2*len(cardBorderRunes))
	for _, c := range cardBorderRunes {
		args = append(args, c, style.Render(c))
	}
	return strings.NewReplacer(args...).Replace(text)
}
