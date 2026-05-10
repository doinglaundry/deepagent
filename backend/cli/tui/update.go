package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"eino-cli/backend/agent/middlewares"
)

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.handleResize(msg)
	case tea.KeyMsg:
		return m.handleKey(msg)
	case chunkMsg:
		return m.handleChunk(msg)
	case doneMsg:
		return m.handleDone(msg)
	case middlewares.DebugEvent:
		return m.handleDebug(msg)
	case spinner.TickMsg:
		if !m.streaming {
			return m, nil // self-terminate the tick chain
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}

	var cmds []tea.Cmd
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

func (m *Model) handleResize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height

	// Layout budget: header(3) + blank(1) + viewport(flex) + stream(0-3) + input(3) + footer(1).
	headerH := 3
	streamH := 0
	if m.streaming || m.lastErr != nil {
		streamH = 3
	}
	inputH := 3
	footerH := 1
	chrome := headerH + 1 + streamH + inputH + footerH

	vpH := msg.Height - chrome
	if vpH < 3 {
		vpH = 3
	}
	m.viewport.Width = msg.Width
	m.viewport.Height = vpH

	m.input.Width = msg.Width - 4 // account for prompt + padding

	// Rebuild markdown at new width so wrapping is correct.
	m.mdRenderer = nil
	for i := range m.messages {
		if m.messages[i].Role == "assistant" {
			m.messages[i].Rendered = m.renderMarkdown(m.messages[i].Content)
		}
	}
	m.rebuildHistory()
	m.ready = true
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyCtrlC:
		if m.abortStream() {
			return m, nil
		}
		if m.pendingExit {
			return m, tea.Quit
		}
		m.pendingExit = true
		m.pushMessage("system", "Press Ctrl-C again to quit, or type /exit.")
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
		return m.submit(text)
	}

	// Feed key to input + viewport (viewport handles PgUp/PgDn/arrows when input doesn't claim them).
	var cmds []tea.Cmd
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

// submit handles slash commands inline; otherwise streams via the runtime.
func (m *Model) submit(text string) (tea.Model, tea.Cmd) {
	if cmd, handled := m.handleBuiltin(text); handled {
		return m, cmd
	}

	m.pushMessage("user", text)
	m.streaming = true
	m.streamBuf.Reset()
	m.lastErr = nil

	var consumer middlewares.DebugConsumer
	if m.debug && m.prog != nil {
		consumer = teaProgramConsumer{p: m.prog}
	}

	ch, cancel, awaitDone := startStream(m.rt, text, consumer)
	m.chunkCh = ch
	m.cancel = cancel
	return m, tea.Batch(waitForChunk(ch), awaitDone, m.spin.Tick)
}

// handleDebug renders a DebugEvent (from Trace middleware) as a scrollback message.
func (m *Model) handleDebug(ev middlewares.DebugEvent) (tea.Model, tea.Cmd) {
	switch ev.Phase {
	case middlewares.DebugBefore:
		m.pushMessage("debug-input", formatDebugInput(ev))
	case middlewares.DebugAfter:
		m.pushMessage("debug-output", formatDebugOutput(ev))
	}
	return m, nil
}

func (m *Model) handleBuiltin(text string) (tea.Cmd, bool) {
	if !strings.HasPrefix(text, "/") {
		return nil, false
	}
	name := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(text, " ", 2)[0], "/"))
	switch strings.ToLower(name) {
	case "exit", "quit":
		return tea.Quit, true
	case "clear":
		m.messages = freshMessages()
		m.rebuildHistory()
		m.rt.ClearHistory()
		return nil, true
	case "debug":
		return m.handleDebugCmd(text), true
	case "help":
		m.pushMessage("user", text)
		m.pushMessage("assistant", builtinHelp())
		return nil, true
	}
	return nil, false
}

// handleDebugCmd processes "/debug [on|off|toggle]"; empty arg toggles.
func (m *Model) handleDebugCmd(text string) tea.Cmd {
	arg := strings.TrimSpace(strings.TrimPrefix(text, "/debug"))
	switch strings.ToLower(arg) {
	case "", "toggle":
		m.debug = !m.debug
	case "on":
		m.debug = true
	case "off":
		m.debug = false
	default:
		m.pushMessage("system", "usage: /debug [on|off|toggle]")
		return nil
	}
	state := "off"
	if m.debug {
		state = "on"
	}
	m.pushMessage("system", fmt.Sprintf("debug = %s", state))
	return nil
}

func (m *Model) handleChunk(msg chunkMsg) (tea.Model, tea.Cmd) {
	m.streamBuf.WriteString(string(msg))
	return m, waitForChunk(m.chunkCh)
}

func (m *Model) handleDone(msg doneMsg) (tea.Model, tea.Cmd) {
	m.streaming = false
	m.cancel = nil
	m.chunkCh = nil

	if msg.err != nil {
		m.lastErr = msg.err
		// Surface any partial content as an assistant message before the error.
		if buf := strings.TrimSpace(m.streamBuf.String()); buf != "" {
			m.pushMessage("assistant", buf)
		}
		m.pushMessage("system", fmt.Sprintf("error: %s", msg.err))
		m.streamBuf.Reset()
		return m, nil
	}

	// Prefer the runtime's authoritative final output over the chunk buffer.
	final := strings.TrimSpace(msg.output)
	if final == "" {
		final = strings.TrimSpace(m.streamBuf.String())
	}
	if final != "" {
		m.pushMessage("assistant", final)
	}
	m.streamBuf.Reset()
	return m, nil
}
