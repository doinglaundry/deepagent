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
	if todoPanel := m.renderTodoPanel(); todoPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(todoPanel)
	}
	if approvalPanel := m.renderApprovalPanel(); approvalPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(approvalPanel)
		return sb.String()
	}
	if streamPanel := m.renderStreamPanel(); streamPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(streamPanel)
	}
	if historyPanel := m.renderRunHistoryPanel(); historyPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(historyPanel)
	}
	if popup := m.renderPopup(); popup != "" {
		sb.WriteString("\n")
		sb.WriteString(popup)
	}
	sb.WriteString("\n")
	sb.WriteString(m.renderInput())
	sb.WriteString("\n")
	sb.WriteString(m.renderFooter())
	return sb.String()
}

// renderApprovalPanel returns the empty string when no HITL request is
// pending; otherwise renders the front of m.hitlQueue. recomputeLayout
// reserves approvalPromptHeight cells (+ separator) when the queue is
// non-empty — keep the rendered line count in lockstep with that
// reservation or the input box drifts.
func (m *Model) renderApprovalPanel() string {
	if len(m.hitlQueue) == 0 {
		return ""
	}
	return renderApprovalPrompt(m.hitlQueue[0], m.width)
}

// renderStreamPanel shows a single-line thinking indicator while a turn is
// in flight. The previous behaviour streamed dim partial output below a
// spinner; we dropped that because (a) half-rendered code fences and
// markdown looked broken, and (b) the indicator-only layout reads as a
// more decisive "the model is working" without competing for attention
// with the input box. m.streamBuf is still populated for handleDone's
// error fallback path — just no longer rendered here.
func (m *Model) renderStreamPanel() string {
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

func (m *Model) renderInput() string {
	value := m.input.Value()
	body := "❯ " + m.renderInputText(value, m.input.Position())
	return inputBorderStyle.Width(m.width).Render(body)
}

func (m *Model) renderInputText(value string, cursor int) string {
	if value == "" {
		placeholder := m.input.Placeholder
		if placeholder == "" {
			placeholder = "Ask anything... (/help for commands)"
		}
		first := " "
		if placeholder != "" {
			first = placeholder[:1]
		}
		return m.input.Cursor.Style.Render(first) + dimStyle.Render(placeholder[1:])
	}
	highlightLen := 0
	if name := highlightedCommandName(value, m.availableCommands()); name != "" {
		highlightLen = len(name) + 1
	}
	var sb strings.Builder
	for i, r := range value {
		ch := string(r)
		if i < highlightLen {
			ch = popupNameStyle.Render(ch)
		}
		if i == cursor {
			ch = m.input.Cursor.Style.Render(ch)
		}
		sb.WriteString(ch)
	}
	if cursor == len(value) {
		sb.WriteString(m.input.Cursor.Style.Render(" "))
	}
	return sb.String()
}

func (m *Model) renderFooter() string {
	left := ""
	if m.tokenTotal > 0 {
		left = footerStyle.Render(formatTokenCount(m.tokenTotal))
	}
	// Streaming shows a single actionable hint; idle is the meta-hint
	// "you can type / for commands". Old footer concatenated three
	// hints, which read as a tutorial banner.
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

// formatTokenCount: >=1000 → "3.4k tokens"; <1000 → "<n> tokens".
// Single decimal place is enough at thousand-scale and stays under 10
// chars so the footer doesn't crowd the right-hand hint at narrow
// widths.
func formatTokenCount(n int64) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk tokens", float64(n)/1000)
	}
	return fmt.Sprintf("%d tokens", n)
}
