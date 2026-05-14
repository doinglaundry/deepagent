package tui

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"
)

type reloadDoneMsg struct {
	err error
}

func (m *Model) handleReloadCmd() tea.Cmd {
	if m.streaming || m.bootstrapLoading {
		m.pushMessage("system", "finish or cancel the current response before /reload")
		return nil
	}
	if m.bootstrap != nil {
		m.pushMessage("system", "finish or cancel SOUL bootstrap before /reload")
		return nil
	}
	m.pushMessage("system", "Reloading agent service...")
	rt := m.rt
	return func() tea.Msg {
		return reloadDoneMsg{err: rt.ReloadSoul(context.Background())}
	}
}

func (m *Model) handleReloadDone(msg reloadDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.pushMessage("system", fmt.Sprintf("reload: %v", msg.err))
		return m, nil
	}
	m.resetConversationView()
	m.pushMessage("system", "Agent service reloaded")
	return m, nil
}

func (m *Model) resetConversationView() {
	m.messages = freshMessages(m.width, m.modelName, m.cwd)
	m.toolBlocks = nil
	m.lastSeenMsgCount = 0
	m.toolBlockSeq = 0
	m.footerHint = ""
	m.flushedMsgCount = 0
	m.pendingScrollback = nil
	m.todos = nil
	m.todoExpanded = false
	m.rebuildHistory()
}
