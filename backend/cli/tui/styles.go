package tui

import "github.com/charmbracelet/lipgloss"

// Theme is intentionally small for v1: a primary accent (used for
// the logo, spinner and assistant prefix), a dim shade for
// secondary text (cwd, footer, prompts), and a plain "user input"
// emphasis. v2+ can pull these from a real theme struct.
var (
	primaryStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("13")) // magenta
	accentStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("12")) // blue
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

	userPrefixStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	assistantPrefixStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10")) // green
	systemPrefixStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))           // orange

	headerTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	footerStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	inputBorderStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.NormalBorder()).
				BorderTop(true).BorderBottom(true).
				BorderLeft(false).BorderRight(false).
				BorderForeground(lipgloss.Color("241")).
				PaddingLeft(0).PaddingRight(0)
)
