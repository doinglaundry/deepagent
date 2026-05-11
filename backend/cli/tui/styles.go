package tui

import "github.com/charmbracelet/lipgloss"

// Minimal v1 theme: primary accent + dim secondary + user emphasis.
var (
	primaryStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("13")) // magenta
	accentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // blue
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

	userPrefixStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	assistantPrefixStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10")) // green
	systemPrefixStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))           // orange

	// User echo block: subtle grey background + bright text so the user's
	// own line reads as a card pressed into the scroll, distinct from the
	// assistant body which floats prefix-only. The whole "❯ <content>"
	// span is rendered through this style in one shot (no nested Render),
	// so the Background ANSI run doesn't get truncated by an inner reset.
	userBlockStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)

	// Debug trace styling: faint body, distinct bold markers for input vs output.
	debugInputMarkerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))  // light blue
	debugOutputMarkerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("207")) // light magenta
	debugBodyStyle         = lipgloss.NewStyle().Faint(true)

	// Thinking indicator: bright magenta "✶", bold-white verb, dim tag.
	thinkingMarkerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	thinkingPresentStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	// Scrollback summary "✻ Verbed for Ns": faint magenta so it reads as
	// a closed chapter rather than competing with the next prompt.
	thinkingSummaryStyle = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("13"))

	// Todo panel styling. Strikethrough on completed items uses ANSI SGR 9;
	// terminals that drop SGR 9 (some old tmux forwards) fall back to
	// dim+green which is still readable.
	todoCompletedStyle  = lipgloss.NewStyle().Faint(true).Strikethrough(true).Foreground(lipgloss.Color("10")) // green + strike + faint
	todoInProgressStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))                     // orange bold
	todoPendingStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))                                // light grey
	todoBarFilledStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))                                 // accent blue
	todoBarEmptyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))                                // dim

	headerTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	footerStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	inputBorderStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.NormalBorder()).
				BorderTop(true).BorderBottom(true).
				BorderLeft(false).BorderRight(false).
				BorderForeground(lipgloss.Color("241")).
				PaddingLeft(0).PaddingRight(0)
)
