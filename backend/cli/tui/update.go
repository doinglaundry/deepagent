package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"eino-cli/backend/agent/middlewares"
)

func (m *Model) Update(msg tea.Msg) (model tea.Model, cmd tea.Cmd) {
	defer func() {
		if model == nil {
			model = m
		}
		if next, ok := model.(*Model); ok {
			cmd = tea.Batch(cmd, flushScrollbackCmd(next))
		}
	}()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return applyResize(m, msg)
	case tea.KeyMsg:
		return applyKey(m, msg)
	case chunkMsg:
		m.streamBuf.WriteString(string(msg))
		return m, waitForStreamMsg(m.streamCh)
	case doneMsg:
		return applyDone(m, msg)
	case dreamDoneMsg:
		return applyDreamDone(m, msg)
	case approvalRequest:
		return applyApprovalRequest(m, msg)
	case middlewares.TraceEvent:
		model, cmd = applyTraceEvent(m, msg)
		if m.streaming {
			cmd = tea.Batch(cmd, waitForStreamMsg(m.streamCh))
		}
		return model, cmd
	case footerHintExpiredMsg:
		m.footerHint = ""
		return m, nil
	case spinner.TickMsg:
		if !m.streaming {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		m.elapsed = time.Since(m.streamStart).Round(time.Second)
		m.shimmerOffset++
		return m, cmd
	}

	var cmds []tea.Cmd
	var nextCmd tea.Cmd
	m.input, nextCmd = m.input.Update(msg)
	cmds = append(cmds, nextCmd)
	m.viewport, nextCmd = m.viewport.Update(msg)
	cmds = append(cmds, nextCmd)
	return m, tea.Batch(cmds...)
}

func applyResize(m *Model, msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	recomputeLayout(m)

	m.mdRenderer = nil
	for i := range m.messages {
		switch m.messages[i].Role {
		case "banner":
			m.messages[i].Content = renderBanner(m.width, m.modelName, m.cwd)
		case "assistant":
			m.messages[i].Rendered = renderMarkdown(m, m.messages[i].Content)
		}
	}
	rebuildHistory(m)
	m.ready = true
	return m, nil
}

func recomputeLayout(m *Model) {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	streamH := 0
	if len(m.hitlQueue) == 0 && (m.streaming || m.lastErr != nil) {
		streamH = 1
	}
	todoH := getTodoPanelHeight(m)
	popupH := getPopupHeight(m)
	approvalH := 0
	runHistoryH := getRunHistoryPanelHeight(m)
	inputH := 3
	footerH := 1
	if len(m.hitlQueue) > 0 {
		approvalH = approvalPromptHeight + 1
		popupH = 0
		runHistoryH = 0
		inputH = 0
		footerH = 0
	}
	chrome := streamH + todoH + popupH + approvalH + runHistoryH + inputH + footerH

	vpMax := m.height - chrome
	if vpMax < 3 {
		vpMax = 3
	}
	m.viewport.Width = m.width

	want := m.viewport.TotalLineCount()
	if want < 1 {
		want = 1
	}
	if want > vpMax {
		want = vpMax
	}
	m.viewport.Height = want
	m.input.Width = m.width - 4
}

func getTodoPanelHeight(m *Model) int {
	if len(m.todos) == 0 {
		return 0
	}
	if !m.todoExpanded {
		return 1
	}
	return 2 + len(m.todos)
}

func applyKey(m *Model, msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if len(m.hitlQueue) > 0 {
		if cmd, handled := applyApprovalKey(m, msg); handled {
			return m, cmd
		}
		if msg.Type == tea.KeyCtrlC {
			abortStream(m)
		}
		return m, nil
	}
	if m.runHistoryOpen {
		if cmd, handled := applyRunHistoryKey(m, msg); handled {
			return m, cmd
		}
		return m, nil
	}

	if getPopupHeight(m) > 0 {
		if cmd, handled := applyPopupKey(m, msg); handled {
			return m, cmd
		}
	}
	switch msg.String() {
	case "alt+b", "esc+b":
		moveInputWord(m, -1)
		return m, nil
	case "alt+f", "esc+f":
		moveInputWord(m, 1)
		return m, nil
	}
	switch msg.Type {
	case tea.KeyCtrlC:
		if abortStream(m) {
			return m, nil
		}
		if m.pendingExit {
			return m, tea.Quit
		}
		m.pendingExit = true
		pushMessage(m, "system", "Press Ctrl-C again to quit, or type /exit.")
		return m, nil
	case tea.KeyEsc:
		if abortStream(m) {
			return m, nil
		}
		m.input.SetValue("")
		return m, nil
	case tea.KeyEnter:
		if m.streaming {
			return m, nil
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		m.input.Reset()
		m.pendingExit = false
		return submit(m, text)
	}
	if msg.String() == "ctrl+o" {
		if block := getLatestCollapsibleToolBlock(m); block != nil {
			if block.flushed {
				m.pendingScrollback = append(m.pendingScrollback, renderExpandedToolBlockCopy(block))
				m.footerHint = "printed expanded tool block"
				return m, expireFooterHint(3 * time.Second)
			}
			block.collapsed = !block.collapsed
			rebuildHistory(m)
			return m, nil
		}
		m.footerHint = "nothing to expand"
		return m, expireFooterHint(3 * time.Second)
	}

	var cmds []tea.Cmd
	var cmd tea.Cmd
	prevValue := m.input.Value()
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	if m.input.Value() != prevValue {
		applyInputChanged(m)
	}
	return m, tea.Batch(cmds...)
}

func applyApprovalRequest(m *Model, req approvalRequest) (tea.Model, tea.Cmd) {
	m.hitlQueue = append(m.hitlQueue, req)
	recomputeLayout(m)
	return m, nil
}

func applyApprovalKey(m *Model, msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.Type {
	case tea.KeyEnter, tea.KeyEsc:
		resolveApproval(m, false)
		return nil, true
	case tea.KeyCtrlC:
		resolveApproval(m, false)
		return nil, false
	case tea.KeyRunes:
		switch strings.ToLower(string(msg.Runes)) {
		case "y":
			resolveApproval(m, true)
			return nil, true
		case "n":
			resolveApproval(m, false)
			return nil, true
		}
	}
	return nil, false
}

func resolveApproval(m *Model, approved bool) {
	if len(m.hitlQueue) == 0 {
		return
	}
	front := m.hitlQueue[0]
	select {
	case front.reply <- approved:
	default:
	}
	m.hitlQueue = m.hitlQueue[1:]
	recomputeLayout(m)
}

func drainApprovals(m *Model) {
	if len(m.hitlQueue) == 0 {
		return
	}
	m.hitlQueue = nil
	recomputeLayout(m)
}

func applyInputChanged(m *Model) {
	matches := getPopupMatches(m)
	if m.popupSel < 0 || m.popupSel >= len(matches) {
		m.popupSel = 0
	}
	recomputeLayout(m)
}

func applyPopupKey(m *Model, msg tea.KeyMsg) (tea.Cmd, bool) {
	matches := getPopupMatches(m)
	if len(matches) == 0 {
		return nil, false
	}
	switch msg.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		m.popupSel = (m.popupSel - 1 + len(matches)) % len(matches)
		return nil, true
	case tea.KeyDown, tea.KeyCtrlN:
		m.popupSel = (m.popupSel + 1) % len(matches)
		return nil, true
	case tea.KeyTab:
		applyPopupSelection(m, matches[m.popupSel])
		applyInputChanged(m)
		return nil, true
	case tea.KeyEnter:
		applyPopupSelection(m, matches[m.popupSel])
		applyInputChanged(m)
		return nil, false
	case tea.KeyEsc:
		m.input.SetValue("")
		applyInputChanged(m)
		return nil, true
	}
	return nil, false
}

func applyPopupSelection(m *Model, c slashCommand) {
	m.input.SetValue("/" + c.Name)
	if c.Type == "builtin" || c.Type == "skill" {
		m.input.SetValue("/" + c.Name + " ")
	}
	m.input.CursorEnd()
}

func submit(m *Model, text string) (tea.Model, tea.Cmd) {
	if cmd, handled := applyBuiltin(m, text); handled {
		return m, cmd
	}

	pushMessage(m, "user", text)
	m.streaming = true
	m.streamBuf.Reset()
	m.lastErr = nil
	m.interrupted = false

	m.verbPresent, m.verbPast = pickVerb()
	m.streamStart = time.Now()
	m.elapsed = 0

	ch, cancel := startStream(m.rt, m.sessionID, text, m.runs)
	m.streamCh = ch
	m.cancel = cancel
	return m, tea.Batch(waitForStreamMsg(ch), m.spin.Tick)
}

func applyTraceEvent(m *Model, ev middlewares.TraceEvent) (tea.Model, tea.Cmd) {
	switch ev.Phase {
	case middlewares.TracePhaseBefore:
		if m.toolBlocksEnabled {
			blocks := extractNewToolBlocks(ev.Messages, m.lastSeenMsgCount, &m.toolBlockSeq, m.toolArgsMaxChars)
			for _, block := range blocks {
				m.toolBlocks = append(m.toolBlocks, block)
				pushToolBlockMessage(m, fmt.Sprintf("%s%d]", toolPlaceholderPrefix, block.id))
			}
		}
		m.lastSeenMsgCount = len(ev.Messages)
	case middlewares.TracePhaseTodos:
		prevH := getTodoPanelHeight(m)
		m.todos = ev.Todos
		if getTodoPanelHeight(m) != prevH {
			recomputeLayout(m)
		}
	case middlewares.TracePhaseTokens:
		if ev.Tokens != nil {
			m.tokenTotal = ev.Tokens.TotalTokens
		}
	}
	return m, nil
}

func pushToolBlockMessage(m *Model, content string) {
	if n := len(m.messages); n > 0 && m.messages[n-1].Role == "assistant" {
		m.messages = append(m.messages, chatMessage{})
		copy(m.messages[n:], m.messages[n-1:])
		m.messages[n-1] = chatMessage{Role: "tool-block", Content: content}
		rebuildHistory(m)
		return
	}
	pushMessage(m, "tool-block", content)
}

func applyDreamDone(m *Model, msg dreamDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		pushMessage(m, "system", fmt.Sprintf("dream: %v", msg.err))
		return m, nil
	}
	if strings.TrimSpace(msg.output) == "" {
		pushMessage(m, "system", "dream: complete")
		return m, nil
	}
	pushMessage(m, "system", msg.output)
	return m, nil
}

func applyDone(m *Model, msg doneMsg) (tea.Model, tea.Cmd) {
	elapsed := time.Since(m.streamStart).Round(time.Second)
	drainedCmd := drainQueuedStreamMessages(m)
	interrupted := m.interrupted

	m.cancel = nil
	m.streamCh = nil
	m.interrupted = false
	drainApprovals(m)

	if msg.err != nil {
		if buf := strings.TrimSpace(m.streamBuf.String()); buf != "" {
			pushMessage(m, "assistant", buf)
		}
		if interrupted {
			m.lastErr = nil
			pushMessage(m, "system", "Conversation interrupted")
		} else {
			m.lastErr = msg.err
			pushMessage(m, "system", fmt.Sprintf("error: %s", msg.err))
		}
		m.streamBuf.Reset()
		m.streaming = false
		queueCompletedTurnScrollback(m)
		return m, drainedCmd
	}

	final := strings.TrimSpace(msg.output)
	if final == "" {
		final = strings.TrimSpace(m.streamBuf.String())
	}
	if final != "" {
		pushMessage(m, "assistant", final)
	}
	if interrupted {
		pushMessage(m, "system", "Conversation interrupted")
	}

	const summaryThreshold = 2 * time.Second
	if !interrupted && elapsed >= summaryThreshold && m.verbPast != "" {
		pushMessage(m, "thinking-summary",
			fmt.Sprintf("%s for %ds", m.verbPast, int(elapsed.Seconds())))
	}

	m.streamBuf.Reset()
	m.streaming = false
	queueCompletedTurnScrollback(m)
	return m, drainedCmd
}

func drainQueuedStreamMessages(m *Model) tea.Cmd {
	var cmds []tea.Cmd
	for m.streamCh != nil {
		select {
		case queued, ok := <-m.streamCh:
			if !ok {
				return tea.Batch(cmds...)
			}
			switch v := queued.(type) {
			case middlewares.TraceEvent:
				_, cmd := applyTraceEvent(m, v)
				cmds = append(cmds, cmd)
			case chunkMsg:
				m.streamBuf.WriteString(string(v))
			}
		default:
			return tea.Batch(cmds...)
		}
	}
	return tea.Batch(cmds...)
}
