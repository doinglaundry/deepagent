package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
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
	case spinner.TickMsg:
		if !m.streaming {
			// Self-terminate the tick chain when nobody's
			// watching: the spinner only renders during a
			// streaming response.
			return m, nil
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

	// Layout budget (top-down):
	//   header        (3 rows)
	//   blank         (1 row)
	//   viewport      (flex)
	//   stream panel  (0-3 rows; reserved while streaming)
	//   input         (3 rows incl. borders)
	//   footer        (1 row)
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

	// Default: feed the keypress to the input box (and the
	// viewport, which handles PgUp/PgDn/arrows for scrolling
	// when the input doesn't claim them).
	var cmds []tea.Cmd
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)
	return m, tea.Batch(cmds...)
}

// submit dispatches the input text: built-in slash commands are
// handled inline; anything else is sent to the runtime via
// startStream and the chunk pump.
func (m *Model) submit(text string) (tea.Model, tea.Cmd) {
	if cmd, handled := m.handleBuiltin(text); handled {
		return m, cmd
	}

	m.pushMessage("user", text)
	m.streaming = true
	m.streamBuf.Reset()
	m.lastErr = nil

	ch, cancel, awaitDone := startStream(m.rt, text)
	m.chunkCh = ch
	m.cancel = cancel
	return m, tea.Batch(waitForChunk(ch), awaitDone, m.spin.Tick)
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
		m.messages = nil
		m.rebuildHistory()
		m.rt.ClearHistory()
		return nil, true
	case "help":
		m.pushMessage("user", text)
		m.pushMessage("assistant", builtinHelp())
		return nil, true
	}
	return nil, false
}

func (m *Model) handleChunk(msg chunkMsg) (tea.Model, tea.Cmd) {
	m.streamBuf.WriteString(string(msg))
	// Schedule the next read on the same channel.
	return m, waitForChunk(m.chunkCh)
}

func (m *Model) handleDone(msg doneMsg) (tea.Model, tea.Cmd) {
	m.streaming = false
	m.cancel = nil
	m.chunkCh = nil

	if msg.err != nil {
		m.lastErr = msg.err
		// If we had any partial content, surface it as an
		// assistant message so the user can see what got
		// produced before the error / abort.
		if buf := strings.TrimSpace(m.streamBuf.String()); buf != "" {
			m.pushMessage("assistant", buf)
		}
		m.pushMessage("system", fmt.Sprintf("error: %s", msg.err))
		m.streamBuf.Reset()
		return m, nil
	}

	// Prefer the runtime's authoritative output over our
	// chunk-accumulated buffer (the runtime collates the final
	// message; chunks may include artefacts the runtime trims).
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
