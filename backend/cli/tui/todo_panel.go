package tui

import (
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk/prebuilt/deep"
)

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
