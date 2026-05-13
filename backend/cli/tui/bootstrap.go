package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	bootstrap "eino-cli/backend/cli/bootstrap"
	"eino-cli/backend/soulbootstrap"
)

type bootstrapReplyMsg struct {
	reply soulbootstrap.Reply
	err   error
}

type bootstrapSavedMsg struct {
	err error
}

func (m *Model) handleBootstrapCmd() tea.Cmd {
	if m.streaming {
		m.pushMessage("system", "finish or cancel the current response before /bootstrap")
		return nil
	}
	if m.cfg == nil {
		m.pushMessage("system", "bootstrap unavailable: missing config")
		return nil
	}
	session, err := bootstrap.NewSession(m.cfg)
	if err != nil {
		m.pushMessage("system", fmt.Sprintf("bootstrap: %v", err))
		return nil
	}
	m.bootstrap = session
	m.pushMessage("system", "Starting SOUL bootstrap. Type /cancel to abort.")
	return m.startBootstrapLoading(m.nextBootstrapReply(""))
}

func (m *Model) submitBootstrap(text string) (tea.Model, tea.Cmd) {
	trimmed := strings.TrimSpace(text)
	if strings.EqualFold(trimmed, "/cancel") {
		m.bootstrap = nil
		m.pushMessage("system", "SOUL bootstrap cancelled")
		return m, nil
	}
	if m.bootstrap.HasDraft() && isBootstrapApproval(trimmed) {
		return m, m.saveBootstrapDraft()
	}
	m.pushMessage("user", text)
	return m, m.startBootstrapLoading(m.nextBootstrapReply(text))
}

func (m *Model) nextBootstrapReply(input string) tea.Cmd {
	session := m.bootstrap
	cfg := m.cfg
	return func() tea.Msg {
		reply, err := session.Next(context.Background(), cfg, input)
		return bootstrapReplyMsg{reply: reply, err: err}
	}
}

func (m *Model) handleBootstrapReply(msg bootstrapReplyMsg) (tea.Model, tea.Cmd) {
	m.bootstrapLoading = false
	m.recomputeLayout()
	if msg.err != nil {
		m.pushMessage("system", fmt.Sprintf("bootstrap: %v", msg.err))
		return m, nil
	}
	content := strings.TrimSpace(msg.reply.Message)
	if msg.reply.Ready && strings.TrimSpace(msg.reply.Draft) != "" {
		content = strings.TrimSpace(content + "\n\n" + msg.reply.Draft + "\n\nDoes this feel right? Type yes to save, or tell me what to change.")
	}
	m.pushMessage("assistant", content)
	return m, nil
}

func (m *Model) startBootstrapLoading(cmd tea.Cmd) tea.Cmd {
	m.bootstrapLoading = true
	m.lastErr = nil
	m.verbPresent, _ = pickVerb()
	m.streamStart = time.Now()
	m.elapsed = 0
	m.recomputeLayout()
	return tea.Batch(cmd, m.spin.Tick)
}

func (m *Model) saveBootstrapDraft() tea.Cmd {
	session := m.bootstrap
	rt := m.rt
	return func() tea.Msg {
		if err := session.Save(); err != nil {
			return bootstrapSavedMsg{err: err}
		}
		if err := rt.ReloadSoul(context.Background()); err != nil {
			return bootstrapSavedMsg{err: err}
		}
		return bootstrapSavedMsg{}
	}
}

func (m *Model) handleBootstrapSaved(msg bootstrapSavedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.pushMessage("system", fmt.Sprintf("bootstrap save: %v", msg.err))
		return m, nil
	}
	m.bootstrap = nil
	m.messages = freshMessages(m.width, m.modelName, m.cwd)
	m.rebuildHistory()
	m.pushMessage("system", "SOUL saved and reloaded")
	return m, nil
}

func isBootstrapApproval(text string) bool {
	switch strings.ToLower(strings.TrimSpace(text)) {
	case "y", "yes", "save", "confirm", "ok":
		return true
	default:
		return false
	}
}
