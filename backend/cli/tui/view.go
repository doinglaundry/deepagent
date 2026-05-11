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
	sb.WriteString(m.renderHeader())
	sb.WriteString("\n\n")
	sb.WriteString(m.viewport.View())
	if streamPanel := m.renderStreamPanel(); streamPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(streamPanel)
	}
	if todoPanel := m.renderTodoPanel(); todoPanel != "" {
		sb.WriteString("\n")
		sb.WriteString(todoPanel)
	}
	sb.WriteString("\n")
	sb.WriteString(m.renderInput())
	sb.WriteString("\n")
	sb.WriteString(m.renderFooter())
	return sb.String()
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
		return fmt.Sprintf("%s %s %s",
			thinkingMarkerStyle.Render("✶"),
			thinkingPresentStyle.Render(m.verbPresent+"…"),
			dimStyle.Render(fmt.Sprintf("(%ds · thinking)", secs)),
		)
	}
	if m.lastErr != nil {
		return errorStyle.Render(fmt.Sprintf("error: %s", m.lastErr))
	}
	return ""
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
