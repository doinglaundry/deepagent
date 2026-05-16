package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
)

func (m *Model) View() string {
	if !m.ready {
		return "Initializing..."
	}

	var sb strings.Builder
	sb.WriteString(m.viewport.View())
	if streamPanel := m.renderStreamPanel(); streamPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(streamPanel)
	}
	if todoPanel := m.renderTodoPanel(); todoPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(todoPanel)
	}
	if approvalPanel := m.renderApprovalPanel(); approvalPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(approvalPanel)
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

// renderTodoPanel returns the empty string when there are no todos so the
// panel disappears cleanly between conversations. Otherwise it renders the
// expanded or collapsed layout based on m.todoExpanded.
func (m *Model) renderTodoPanel() string {
	if len(m.todos) == 0 {
		return ""
	}
	if m.todoExpanded {
		return renderTodoPanelExpanded(m.todos)
	}
	return renderTodoPanelCollapsed(m.todos)
}

// renderTodoPanelCollapsed: single line. Format:
//
//	▶ Todos 2/5 · in_progress: Write reminder middleware
//
// Falls back to "all done" / "N pending" when no in_progress item exists.
func renderTodoPanelCollapsed(todos []deep.TODO) string {
	done, total := countTodos(todos)
	prefix := headerTitleStyle.Render("▶ Todos")
	progress := fmt.Sprintf("%d/%d", done, total)

	var detail string
	if cur := findInProgress(todos); cur != "" {
		detail = todoInProgressStyle.Render("in_progress:") + " " + cur
	} else if done == total {
		detail = todoCompletedStyle.Render("all done")
	} else {
		pending := total - done
		detail = todoPendingStyle.Render(fmt.Sprintf("%d pending", pending))
	}
	return fmt.Sprintf("%s %s · %s", prefix, progress, detail)
}

// renderTodoPanelExpanded: borderless multi-line list with progress bar.
// Each item is "  <symbol> <styled content>". completed items get
// strikethrough via todoCompletedStyle.
func renderTodoPanelExpanded(todos []deep.TODO) string {
	done, total := countTodos(todos)
	header := fmt.Sprintf("  %s · %d/%d  %s",
		headerTitleStyle.Render("Todos"),
		done, total,
		renderTodoBar(done, total, 5),
	)

	lines := []string{header, ""}
	for _, t := range todos {
		lines = append(lines, "  "+renderTodoLine(t))
	}
	return strings.Join(lines, "\n")
}

// renderTodoBar prints `width` cells, filled proportionally to done/total.
// totals==0 is treated as 0/0 (all empty).
func renderTodoBar(done, total, width int) string {
	if width <= 0 {
		return ""
	}
	filled := 0
	if total > 0 {
		filled = (done * width) / total
		if filled > width {
			filled = width
		}
	}
	return todoBarFilledStyle.Render(strings.Repeat("▰", filled)) +
		todoBarEmptyStyle.Render(strings.Repeat("▱", width-filled))
}

func renderTodoLine(t deep.TODO) string {
	switch t.Status {
	case "completed":
		return todoCompletedStyle.Render("✓ " + t.Content)
	case "in_progress":
		return todoInProgressStyle.Render("◐ "+t.Content) +
			todoPendingStyle.Render("  in_progress")
	default: // pending or unknown
		return todoPendingStyle.Render("○ " + t.Content)
	}
}

func countTodos(todos []deep.TODO) (done, total int) {
	total = len(todos)
	for _, t := range todos {
		if t.Status == "completed" {
			done++
		}
	}
	return
}

func findInProgress(todos []deep.TODO) string {
	for _, t := range todos {
		if t.Status == "in_progress" {
			return t.Content
		}
	}
	return ""
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
	left := footerStyle.Render(m.modelName)
	// Token total rides the left segment with modelName because both
	// are session metadata; pushing it into a third center segment
	// would force a 3-way gap calculation. Hidden when 0 so empty
	// sessions stay quiet.
	if m.tokenTotal > 0 {
		left += footerStyle.Render(" · " + formatTokenCount(m.tokenTotal))
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
