package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

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
	case middlewares.TraceEvent:
		return m.handleTraceEvent(msg)
	case spinner.TickMsg:
		if !m.streaming {
			return m, nil // self-terminate the tick chain
		}
		// Spinner is no longer rendered (the thinking indicator owns
		// the line), but its 100ms tick still drives elapsed-second
		// refresh — cheaper than spinning up a dedicated tea.Tick.
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		m.elapsed = time.Since(m.streamStart).Round(time.Second)
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

	m.recomputeLayout()

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

// recomputeLayout sizes viewport / input from m.width / m.height and the
// current panel states (stream + todo). Called from handleResize and from
// any handler that flips a panel-affecting flag (todos / todoExpanded /
// streaming). Cheap enough to run on every relevant edge.
func (m *Model) recomputeLayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	// Layout budget: header(3) + blank(1) + viewport(flex) + stream(0-3)
	// + todoPanel(0..N) + input(3) + footer(1).
	headerH := 3
	streamH := 0
	if m.streaming || m.lastErr != nil {
		streamH = 1 // single-line thinking indicator (was 3 with preview)
	}
	todoH := m.todoPanelHeight()
	inputH := 3
	footerH := 1
	chrome := headerH + 1 + streamH + todoH + inputH + footerH

	vpH := m.height - chrome
	if vpH < 3 {
		vpH = 3
	}
	m.viewport.Width = m.width
	m.viewport.Height = vpH
	m.input.Width = m.width - 4
}

// todoPanelHeight matches what renderTodoPanel actually emits:
//
//	0 lines when len(todos) == 0
//	1 line when collapsed
//	2 + len(todos) lines when expanded (header + blank + one line per todo)
//
// Drift between this and the renderer would silently misalign chrome.
func (m *Model) todoPanelHeight() int {
	if len(m.todos) == 0 {
		return 0
	}
	if !m.todoExpanded {
		return 1
	}
	return 2 + len(m.todos)
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

	// Pick a verb pair once per turn; the present form drives the live
	// indicator and the past form lands in scrollback when handleDone
	// surfaces a thinking-summary above the threshold.
	m.verbPresent, m.verbPast = pickVerb()
	m.streamStart = time.Now()
	m.elapsed = 0

	// Always attach the trace consumer regardless of m.debug: the Todos
	// phase fires on every after-model hook with active todos and must
	// reach the TUI even when /debug is off. Before/After phases are
	// filtered out by handleTraceEvent on the consume side, so the only
	// cost of "always-on" is one extra channel send per turn.
	var consumer middlewares.TraceConsumer
	if m.prog != nil {
		consumer = teaProgramConsumer{p: m.prog}
	}

	ch, cancel, awaitDone := startStream(m.rt, text, consumer)
	m.chunkCh = ch
	m.cancel = cancel
	return m, tea.Batch(waitForChunk(ch), awaitDone, m.spin.Tick)
}

// handleTraceEvent dispatches a TraceEvent. Before/After are pure debug
// surfaces and stay gated by m.debug; Todos updates the panel cache
// unconditionally — the panel is a first-class TUI affordance, not a
// debug aid.
func (m *Model) handleTraceEvent(ev middlewares.TraceEvent) (tea.Model, tea.Cmd) {
	switch ev.Phase {
	case middlewares.TracePhaseBefore:
		if m.debug {
			m.pushMessage("debug-input", formatDebugInput(ev))
		}
	case middlewares.TracePhaseAfter:
		if m.debug {
			m.pushMessage("debug-output", formatDebugOutput(ev))
		}
	case middlewares.TracePhaseTodos:
		prevH := m.todoPanelHeight()
		m.todos = ev.Todos
		// Only relayout when the panel's height actually changes; avoids
		// a viewport reflow on every status flip within an unchanged list.
		if m.todoPanelHeight() != prevH {
			m.recomputeLayout()
		}
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
		// Todos live as long as the conversation thread does; clearing
		// history without clearing the panel would leave a stale list
		// referencing tasks the model no longer remembers.
		hadTodos := len(m.todos) > 0
		m.todos = nil
		m.todoExpanded = false
		if hadTodos {
			m.recomputeLayout()
		}
		return nil, true
	case "debug":
		return m.handleDebugCmd(text), true
	case "plan":
		return m.handlePlanCmd(text), true
	case "todos":
		return m.handleTodosCmd(text), true
	case "help":
		m.pushMessage("user", text)
		m.pushMessage("assistant", builtinHelp())
		return nil, true
	}
	return nil, false
}

// handlePlanCmd processes "/plan [on|off|toggle]"; empty arg toggles.
// On state change it asks the runtime to flip plan mode (which rebuilds
// the lead agent under the runtime's lock). On rebuild failure the
// view-side flag stays in sync with the runtime's rollback so
// successive /plan commands aren't operating on a lie.
func (m *Model) handlePlanCmd(text string) tea.Cmd {
	arg := strings.TrimSpace(strings.TrimPrefix(text, "/plan"))
	target := m.planMode
	switch strings.ToLower(arg) {
	case "", "toggle":
		target = !m.planMode
	case "on":
		target = true
	case "off":
		target = false
	default:
		m.pushMessage("system", "usage: /plan [on|off|toggle]")
		return nil
	}

	if target == m.planMode {
		m.pushMessage("system", fmt.Sprintf("plan = %s", boolWord(m.planMode)))
		return nil
	}

	if err := m.rt.SetPlanMode(context.Background(), target); err != nil {
		m.pushMessage("system", fmt.Sprintf("plan toggle failed: %s", err))
		return nil
	}
	m.planMode = target
	m.pushMessage("system", fmt.Sprintf("plan = %s", boolWord(m.planMode)))
	return nil
}

func boolWord(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// handleTodosCmd processes "/todos [open|close|toggle]"; empty arg toggles.
// "open" / "close" use those words (not on/off) to match the visual model
// (a panel is open or closed, not on or off).
func (m *Model) handleTodosCmd(text string) tea.Cmd {
	arg := strings.TrimSpace(strings.TrimPrefix(text, "/todos"))
	prevH := m.todoPanelHeight()
	switch strings.ToLower(arg) {
	case "", "toggle":
		m.todoExpanded = !m.todoExpanded
	case "open":
		m.todoExpanded = true
	case "close":
		m.todoExpanded = false
	default:
		m.pushMessage("system", "usage: /todos [open|close|toggle]")
		return nil
	}
	if m.todoPanelHeight() != prevH {
		m.recomputeLayout()
	}
	state := "closed"
	if m.todoExpanded {
		state = "open"
	}
	m.pushMessage("system", fmt.Sprintf("todos panel = %s", state))
	return nil
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
	// Snapshot elapsed BEFORE flipping streaming off so a slow handleDone
	// doesn't drift the summary downward.
	elapsed := time.Since(m.streamStart).Round(time.Second)

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
		// Error path skips the thinking-summary — the error line is
		// already enough noise; "Verbed for 3s" on top reads as gloating.
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

	// Short turns (< summaryThreshold) don't warrant a "for 0s" summary —
	// visual noise without info. The 2s line is a single point of tuning
	// — if user feedback says even 2s is too chatty, raise the bar here.
	const summaryThreshold = 2 * time.Second
	if elapsed >= summaryThreshold && m.verbPast != "" {
		m.pushMessage("thinking-summary",
			fmt.Sprintf("%s for %ds", m.verbPast, int(elapsed.Seconds())))
	}

	m.streamBuf.Reset()
	return m, nil
}
