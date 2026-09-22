package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m *Model) View() string {
	if !m.ready {
		return "Initializing..."
	}

	var sb strings.Builder
	sb.WriteString(m.viewport.View())
	if todoPanel := renderTodoPanel(m); todoPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(todoPanel)
	}
	if approvalPanel := renderApprovalPanel(m); approvalPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(approvalPanel)
		return sb.String()
	}
	if streamPanel := renderStreamPanel(m); streamPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(streamPanel)
	}
	if historyPanel := renderRunHistoryPanel(m); historyPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(historyPanel)
	}
	if popup := renderPopup(m); popup != "" {
		sb.WriteString("\n")
		sb.WriteString(popup)
	}
	sb.WriteString("\n")
	sb.WriteString(renderInput(m))
	sb.WriteString("\n")
	sb.WriteString(renderFooter(m))
	return sb.String()
}

func renderApprovalPanel(m *Model) string {
	if len(m.hitlQueue) == 0 {
		return ""
	}
	return renderApprovalPrompt(m.hitlQueue[0], m.width)
}

func renderStreamPanel(m *Model) string {
	if m.streaming {
		secs := int(m.elapsed.Seconds())
		verb := renderShimmer(m.verbPresent+"…", m.shimmerOffset,
			thinkingPresentStyle, thinkingShimmerStyle)
		return fmt.Sprintf("%s %s %s",
			thinkingMarkerStyle.Render("✶"),
			verb,
			dimStyle.Render(fmt.Sprintf("(%ds · thinking)", secs)),
		)
	}
	if m.lastErr != nil {
		return errorStyle.Render(fmt.Sprintf("error: %s", m.lastErr))
	}
	return ""
}

func renderInput(m *Model) string {
	value := m.input.Value()
	body := "❯ " + renderInputText(m, value, m.input.Position())
	return inputBorderStyle.Width(m.width).Render(body)
}

func renderInputText(m *Model, value string, cursor int) string {
	if value == "" {
		placeholder := m.input.Placeholder
		if placeholder == "" {
			placeholder = "Ask anything... (/help for commands)"
		}
		first := " "
		if placeholder != "" {
			first = placeholder[:1]
		}
		m.input.Cursor.SetChar(first)
		return m.input.Cursor.View() + dimStyle.Render(placeholder[1:])
	}
	highlightLen := 0
	if name := highlightedCommandName(value, getAvailableCommands(m)); name != "" {
		highlightLen = len(name) + 1
	}
	var sb strings.Builder
	runes := []rune(value)
	for i, r := range runes {
		ch := string(r)
		if i < highlightLen {
			ch = popupNameStyle.Render(ch)
		}
		if i == cursor {
			m.input.Cursor.SetChar(string(r))
			ch = m.input.Cursor.View()
		}
		sb.WriteString(ch)
	}
	if cursor == len(runes) {
		m.input.Cursor.SetChar(" ")
		sb.WriteString(m.input.Cursor.View())
	}
	return sb.String()
}

func renderFooter(m *Model) string {
	left := ""
	if m.tokenTotal > 0 {
		left = footerStyle.Render(formatTokenCount(m.tokenTotal))
	}
	hint := "/help · ctrl-c to quit"
	if m.streaming {
		hint = "esc to interrupt"
	} else if m.footerHint != "" {
		hint = m.footerHint
	}
	right := footerStyle.Render(hint)
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

func formatTokenCount(n int64) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk tokens", float64(n)/1000)
	}
	return fmt.Sprintf("%d tokens", n)
}
