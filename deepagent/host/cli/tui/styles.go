package tui

import "github.com/charmbracelet/lipgloss"

var (
	primaryStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))

	userPrefixStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	assistantPrefixStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	systemPrefixStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))

	userBlockStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("15")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)

	toolHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	toolBodyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	toolFooterStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	thinkingMarkerStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	thinkingPresentStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	thinkingShimmerStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	thinkingSummaryStyle = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("13"))

	todoCompletedStyle  = lipgloss.NewStyle().Faint(true).Strikethrough(true).Foreground(lipgloss.Color("10"))
	todoInProgressStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	todoPendingStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	todoBarFilledStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	todoBarEmptyStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	headerTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	footerStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))

	popupNameStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	popupArgsStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	popupDescStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	popupRowStyle    = lipgloss.NewStyle().PaddingLeft(2)
	popupSelectedRow = lipgloss.NewStyle().
				Background(lipgloss.Color("236")).
				Foreground(lipgloss.Color("15")).
				PaddingLeft(2).PaddingRight(1)

	inputBorderStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.NormalBorder()).
				BorderTop(true).BorderBottom(true).
				BorderLeft(false).BorderRight(false).
				BorderForeground(lipgloss.Color("241")).
				PaddingLeft(0).PaddingRight(0)
)
