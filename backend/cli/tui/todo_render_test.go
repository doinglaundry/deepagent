package tui

import (
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk/prebuilt/deep"

	"eino-cli/backend/agent/middlewares"
)

func sampleTodos() []deep.TODO {
	return []deep.TODO{
		{Content: "Read deer-flow source", Status: "completed"},
		{Content: "Compare with eino prebuilt", Status: "completed"},
		{Content: "Write reminder middleware", Status: "in_progress"},
		{Content: "Wire /plan slash command", Status: "pending"},
		{Content: "Update tests", Status: "pending"},
	}
}

func TestRenderTodoPanel_EmptyTodosReturnsEmpty(t *testing.T) {
	m := &Model{}
	if got := renderTodoPanel(m); got != "" {
		t.Errorf("empty todos must produce no panel; got %q", got)
	}
}

func TestRenderTodoPanel_CollapsedSingleLine(t *testing.T) {
	m := &Model{todos: sampleTodos(), todoExpanded: false}
	got := renderTodoPanel(m)
	if strings.Count(got, "\n") != 0 {
		t.Errorf("collapsed panel must be single-line; got %d newlines: %q", strings.Count(got, "\n"), got)
	}
	for _, want := range []string{"Todos", "2/5", "in_progress:", "Write reminder middleware"} {
		if !strings.Contains(got, want) {
			t.Errorf("collapsed panel missing %q; got: %q", want, got)
		}
	}
}

func TestRenderTodoPanel_CollapsedAllDone(t *testing.T) {
	todos := []deep.TODO{
		{Content: "A", Status: "completed"},
		{Content: "B", Status: "completed"},
	}
	m := &Model{todos: todos, todoExpanded: false}
	got := renderTodoPanel(m)
	if !strings.Contains(got, "all done") {
		t.Errorf("collapsed panel must say 'all done' when 100%% complete; got: %q", got)
	}
}

func TestRenderTodoPanel_CollapsedNoInProgress(t *testing.T) {
	todos := []deep.TODO{
		{Content: "A", Status: "pending"},
		{Content: "B", Status: "pending"},
		{Content: "C", Status: "completed"},
	}
	m := &Model{todos: todos, todoExpanded: false}
	got := renderTodoPanel(m)
	if !strings.Contains(got, "2 pending") {
		t.Errorf("collapsed panel must say 'N pending' when no in_progress; got: %q", got)
	}
}

func TestRenderTodoPanel_ExpandedShowsAllItems(t *testing.T) {
	m := &Model{todos: sampleTodos(), todoExpanded: true}
	got := renderTodoPanel(m)
	for _, t1 := range sampleTodos() {
		if !strings.Contains(got, t1.Content) {
			t.Errorf("expanded panel missing %q; got:\n%s", t1.Content, got)
		}
	}
	if !strings.Contains(got, "Todos") || !strings.Contains(got, "2/5") {
		t.Errorf("expanded panel missing header / progress; got:\n%s", got)
	}
}

// todoCompletedStyle must carry Strikethrough — lipgloss honours it via
// ANSI SGR 9 at render time. We assert at the style level (not on
// rendered output) because lipgloss disables ANSI in non-TTY tests, so
// scanning for "\x1b[9m" would give a flaky false negative.
func TestTodoCompletedStyle_HasStrikethrough(t *testing.T) {
	if !todoCompletedStyle.GetStrikethrough() {
		t.Errorf("todoCompletedStyle must enable Strikethrough; a regression here makes completed todos visually identical to pending ones")
	}
}

func TestTodoPanelHeight_MatchesRenderer(t *testing.T) {
	tests := []struct {
		name     string
		todos    []deep.TODO
		expanded bool
		wantH    int
	}{
		{"empty", nil, false, 0},
		{"empty-expanded", nil, true, 0},
		{"collapsed", sampleTodos(), false, 1},
		{"expanded-5", sampleTodos(), true, 7}, // 2 + 5 items
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &Model{todos: tc.todos, todoExpanded: tc.expanded}
			if h := getTodoPanelHeight(m); h != tc.wantH {
				t.Errorf("todoPanelHeight = %d, want %d", h, tc.wantH)
			}
			// Sanity: actual rendered line count must match the claim
			// (a drift here would silently break viewport sizing).
			rendered := renderTodoPanel(m)
			gotLines := 0
			if rendered != "" {
				gotLines = strings.Count(rendered, "\n") + 1
			}
			if gotLines != tc.wantH {
				t.Errorf("renderer emitted %d lines, todoPanelHeight claimed %d", gotLines, tc.wantH)
			}
		})
	}
}

func TestHandleTraceEvent_TodosUpdate(t *testing.T) {
	m := &Model{messages: freshMessages(0, "", "")}
	ev := middlewares.TraceEvent{
		Phase: middlewares.TracePhaseTodos,
		Todos: sampleTodos(),
	}
	_, _ = applyTraceEvent(m,ev)
	if len(m.todos) != len(sampleTodos()) {
		t.Errorf("m.todos must update from trace event; got len=%d", len(m.todos))
	}
}

func TestHandleTodosCmd_Toggle(t *testing.T) {
	m := &Model{todos: sampleTodos(), messages: freshMessages(0, "", "")}

	handleTodosCommand(m,"/todos")
	if !m.todoExpanded {
		t.Errorf("/todos must toggle from collapsed → expanded")
	}
	handleTodosCommand(m,"/todos")
	if m.todoExpanded {
		t.Errorf("/todos must toggle from expanded → collapsed")
	}
}

func TestHandleTodosCmd_ExplicitOpenClose(t *testing.T) {
	m := &Model{todos: sampleTodos(), messages: freshMessages(0, "", "")}

	handleTodosCommand(m,"/todos open")
	if !m.todoExpanded {
		t.Errorf("/todos open must expand")
	}
	handleTodosCommand(m,"/todos open") // idempotent
	if !m.todoExpanded {
		t.Errorf("repeated /todos open must stay expanded")
	}
	handleTodosCommand(m,"/todos close")
	if m.todoExpanded {
		t.Errorf("/todos close must collapse")
	}
}

func TestHandleTodosCmd_BadArg(t *testing.T) {
	m := &Model{todos: sampleTodos(), messages: freshMessages(0, "", "")}
	handleTodosCommand(m,"/todos banana")
	last := m.messages[len(m.messages)-1]
	if last.Role != "system" || !strings.Contains(last.Content, "usage:") {
		t.Errorf("bad arg should surface usage; got %+v", last)
	}
}
