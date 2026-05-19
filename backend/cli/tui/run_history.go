package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"eino-cli/backend/session/runs"
)

const runHistoryMaxRows = 8
const runHistorySuccessStatus = "success"

func (m *Model) handleHistoryCmd() tea.Cmd {
	rows, err := m.runs.ListRuns(context.Background())
	if err != nil {
		m.pushMessage("system", fmt.Sprintf("history: %v", err))
		return nil
	}
	if len(rows) == 0 {
		m.pushMessage("system", "history: no runs yet")
		return nil
	}
	m.runHistoryRows = rows
	m.runHistorySel = 0
	m.runHistoryOpen = true
	m.input.Reset()
	m.recomputeLayout()
	return nil
}

func (m *Model) handleRunHistoryKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	if len(m.runHistoryRows) == 0 {
		m.closeRunHistory()
		return nil, true
	}
	switch msg.Type {
	case tea.KeyEsc:
		m.closeRunHistory()
		return nil, true
	case tea.KeyEnter:
		m.rollbackSelectedRun()
		return nil, true
	case tea.KeyUp, tea.KeyCtrlP:
		m.runHistorySel = (m.runHistorySel - 1 + len(m.runHistoryRows)) % len(m.runHistoryRows)
		return nil, true
	case tea.KeyDown, tea.KeyCtrlN:
		m.runHistorySel = (m.runHistorySel + 1) % len(m.runHistoryRows)
		return nil, true
	case tea.KeyRunes:
		if strings.EqualFold(string(msg.Runes), "q") {
			m.closeRunHistory()
			return nil, true
		}
	}
	return nil, false
}

func (m *Model) closeRunHistory() {
	m.runHistoryOpen = false
	m.runHistoryRows = nil
	m.runHistorySel = 0
	m.recomputeLayout()
}

func (m *Model) rollbackSelectedRun() {
	if m.runHistorySel < 0 || m.runHistorySel >= len(m.runHistoryRows) {
		m.closeRunHistory()
		return
	}
	selected := m.runHistoryRows[m.runHistorySel]
	if !selected.Rollbackable {
		msg := "history: selected run is not rollbackable"
		if selected.RollbackError != "" {
			msg += ": " + selected.RollbackError
		}
		m.pushMessage("system", msg)
		m.closeRunHistory()
		return
	}
	history, err := m.runs.RestoreSnapshot(context.Background(), selected.ID)
	if err != nil {
		m.pushMessage("system", fmt.Sprintf("rollback: %v", err))
		m.closeRunHistory()
		return
	}
	if err := m.rt.RollbackToHistory(history); err != nil {
		m.pushMessage("system", fmt.Sprintf("rollback: %v", err))
		m.closeRunHistory()
		return
	}
	rows, _ := m.runs.ListRuns(context.Background())
	m.rebuildAfterRollback(selected, rows)
	m.closeRunHistory()
	m.pushMessage("system", fmt.Sprintf("rolled back to %s", shortRunID(selected.ID)))
}

func (m *Model) rebuildAfterRollback(selected runs.Record, rows []runs.Record) {
	m.messages = freshMessages(m.width, m.modelName, m.cwd)
	m.toolBlocks = nil
	m.lastSeenMsgCount = 0
	m.toolBlockSeq = 0
	m.flushedMsgCount = 0
	m.pendingScrollback = nil
	m.todos = nil
	m.tokenTotal = 0
	m.streamBuf.Reset()
	m.streaming = false
	m.cancel = nil
	m.streamCh = nil

	var kept []runs.Record
	for _, row := range rows {
		if row.CreatedAt.After(selected.CreatedAt) {
			continue
		}
		if row.Status == runHistorySuccessStatus && strings.TrimSpace(row.Output) != "" {
			kept = append(kept, row)
		}
	}
	for i := len(kept) - 1; i >= 0; i-- {
		m.pushMessage("user", kept[i].Prompt)
		m.pushMessage("assistant", kept[i].Output)
	}
}

func (m *Model) runHistoryPanelHeight() int {
	panel := m.renderRunHistoryPanel()
	if panel == "" {
		return 0
	}
	return strings.Count(panel, "\n") + 1
}

func (m *Model) renderRunHistoryPanel() string {
	if !m.runHistoryOpen {
		return ""
	}
	lines := []string{headerTitleStyle.Render("Run history")}
	start := runHistoryWindowStart(m.runHistorySel, len(m.runHistoryRows))
	end := min(len(m.runHistoryRows), start+runHistoryMaxRows)
	for i := start; i < end; i++ {
		row := renderRunHistoryRow(m.runHistoryRows[i], i == m.runHistorySel, m.width)
		lines = append(lines, row)
	}
	if len(m.runHistoryRows) > runHistoryMaxRows {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("  showing %d-%d of %d", start+1, end, len(m.runHistoryRows))))
	}
	lines = append(lines, dimStyle.Render("  enter rollback · esc/q close"))
	return strings.Join(lines, "\n")
}

func runHistoryWindowStart(selected, total int) int {
	if total <= runHistoryMaxRows || selected < runHistoryMaxRows {
		return 0
	}
	start := selected - runHistoryMaxRows + 1
	if maxStart := total - runHistoryMaxRows; start > maxStart {
		return maxStart
	}
	return start
}

func renderRunHistoryRow(rec runs.Record, selected bool, width int) string {
	status := rec.Status
	if rec.Rollbackable {
		status += " rollback"
	}
	body := fmt.Sprintf("%s · %s · %s · %s · %s",
		shortRunID(rec.ID),
		status,
		formatRunHistoryTime(rec.CreatedAt),
		formatRunHistoryDuration(rec.DurationMS),
		truncateHistoryPrompt(rec.Prompt, max(16, width-48)),
	)
	if rec.Tokens > 0 {
		body += " · " + formatTokenCount(rec.Tokens)
	}
	if selected {
		return popupSelectedRow.Render(body)
	}
	return popupRowStyle.Render(body)
}

func shortRunID(id string) string {
	if len(id) <= 10 {
		return id
	}
	return id[:10]
}

func formatRunHistoryTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format("15:04:05")
}

func formatRunHistoryDuration(ms int64) string {
	if ms <= 0 {
		return "0ms"
	}
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

func truncateHistoryPrompt(prompt string, maxChars int) string {
	prompt = strings.TrimSpace(strings.ReplaceAll(prompt, "\n", " "))
	if len(prompt) <= maxChars {
		return prompt
	}
	if maxChars <= 3 {
		return prompt[:maxChars]
	}
	return prompt[:maxChars-3] + "..."
}
