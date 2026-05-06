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
	sb.WriteString(m.renderHeader())
	sb.WriteString("\n\n")
	sb.WriteString(m.viewport.View())
	if streamPanel := m.renderStreamPanel(); streamPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(streamPanel)
	}
	sb.WriteString("\n")
	sb.WriteString(m.renderInput())
	sb.WriteString("\n")
	sb.WriteString(m.renderFooter())
	return sb.String()
}

func (m *Model) renderHeader() string {
	title := headerTitleStyle.Render("eino-tui")
	model := dimStyle.Render(m.modelName)
	cwd := dimStyle.Render(m.cwd)
	if m.cwd == "" {
		cwd = dimStyle.Render(".")
	}
	left := lipgloss.JoinVertical(lipgloss.Left, title, model, cwd)
	return lipgloss.JoinHorizontal(lipgloss.Top, "  ", left)
}

func (m *Model) renderStreamPanel() string {
	if m.streaming {
		body := strings.TrimSpace(m.streamBuf.String())
		spin := m.spin.View()
		header := fmt.Sprintf("%s %s", spin, accentStyle.Render("Thinking..."))
		if body == "" {
			return header
		}
		// While streaming we render plain text (no markdown) for
		// speed and to avoid half-rendered code fences. The final
		// message is re-rendered as markdown in handleDone.
		body = truncateForStream(body, m.viewport.Width, 6)
		return lipgloss.JoinVertical(lipgloss.Left, header, dimStyle.Render(body))
	}
	if m.lastErr != nil {
		return errorStyle.Render(fmt.Sprintf("error: %s", m.lastErr))
	}
	return ""
}

// truncateForStream keeps the streaming preview to the most
// recent N lines so the streaming panel doesn't push the input
// off-screen on long replies.
func truncateForStream(s string, width, maxLines int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= maxLines {
		return s
	}
	tail := lines[len(lines)-maxLines:]
	return "…\n" + strings.Join(tail, "\n")
}

func (m *Model) renderInput() string {
	return inputBorderStyle.Width(m.width).Render(m.input.View())
}

func (m *Model) renderFooter() string {
	left := footerStyle.Render(m.modelName)
	hint := "Enter to send · /help for commands · Ctrl-C to abort/quit"
	if m.streaming {
		hint = "Streaming... · Ctrl-C to abort"
	}
	right := footerStyle.Render(hint)
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}
