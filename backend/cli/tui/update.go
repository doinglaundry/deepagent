package tui

import (
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

	// Rebuild markdown at new width so wrapping is correct. Banner is
	// pre-rendered (verbatim in renderMessage), so a width change has to
	// re-bake its content here too — otherwise a session that started
	// narrow would stay on the compact fallback forever even after the
	// user maximises the window (boxed form expects width >= 80).
	m.mdRenderer = nil
	for i := range m.messages {
		switch m.messages[i].Role {
		case "banner":
			m.messages[i].Content = renderBanner(m.width, m.modelName, m.cwd)
		case "assistant":
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
//
// viewport.Height shrinks to fit the actual content so the input box sits
// flush under the last message instead of being padded to the screen
// bottom with blank lines (Claude Code-style "input glued to content").
// Once content exceeds the available budget the height is clamped and
// viewport starts scrolling normally.
func (m *Model) recomputeLayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	// Layout budget: header(3) + blank(1) + viewport(flex) + stream(0-1)
	// + todoPanel(0..N) + popup(0..popupMaxRows+1) + input(3) + footer(1).
	headerH := 3
	streamH := 0
	if m.streaming || m.lastErr != nil {
		streamH = 1 // single-line thinking indicator (was 3 with preview)
	}
	todoH := m.todoPanelHeight()
	popupH := m.popupHeight()
	inputH := 3
	footerH := 1
	chrome := headerH + 1 + streamH + todoH + popupH + inputH + footerH

	vpMax := m.height - chrome
	if vpMax < 3 {
		vpMax = 3
	}
	m.viewport.Width = m.width

	// Fit-to-content: shrink down to actual line count when below max,
	// otherwise clamp at max and let viewport scroll. TotalLineCount
	// reflects whatever was last SetContent'd, so callers that mutate
	// content (rebuildHistory) must invoke recomputeLayout afterwards.
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
	// Popup-active keys claim priority. With the menu up, Up/Down,
	// Tab, Esc and Enter all mean things specific to the popup; falling
	// through to textinput would (a) hide-move the cursor invisibly and
	// (b) submit half-typed command names. Enter is special: the popup
	// rewrites input to the full /<name> and returns handled=false so
	// the outer KeyEnter below picks it up and runs submit normally
	// (one path through submit, not two).
	if m.popupShown() {
		if cmd, handled := m.handlePopupKey(msg); handled {
			return m, cmd
		}
	}

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
	case tea.KeyEsc:
		// Streaming → ESC aborts the in-flight call. Idle → clear the
		// input so the user gets a fresh prompt instead of mid-typed
		// garbage (mirrors Ctrl-U in most shells).
		if m.abortStream() {
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
		return m.submit(text)
	}

	// Feed key to input + viewport (viewport handles PgUp/PgDn/arrows when input doesn't claim them).
	var cmds []tea.Cmd
	var cmd tea.Cmd
	prevValue := m.input.Value()
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	m.viewport, cmd = m.viewport.Update(msg)
	cmds = append(cmds, cmd)

	// Re-derive popup state on any value edge so backspacing past "/"
	// or typing "/" mid-edit collapses / opens the menu and rebalances
	// chrome height before the next View.
	if m.input.Value() != prevValue {
		m.onInputChanged()
	}
	return m, tea.Batch(cmds...)
}

// popupShown is the runtime equivalent of "shouldShowPopup AND has
// matches"; keyboard routing gates on this.
func (m *Model) popupShown() bool {
	return m.popupHeight() > 0
}

// onInputChanged runs after any keypress that mutated the input value.
// Clamps popupSel into the (possibly shrunken) match range and asks
// the layout to rebalance — popup growth/shrink shifts the viewport
// budget, and missing this leaves the input box at the wrong y.
func (m *Model) onInputChanged() {
	matches := filterCommands(commands, m.input.Value())
	if m.popupSel < 0 || m.popupSel >= len(matches) {
		m.popupSel = 0
	}
	m.recomputeLayout()
}

// handlePopupKey routes keys while the popup is visible. Returns
// (cmd, true) when the popup owned the key. KeyEnter intentionally
// returns (nil, false) after rewriting input so the outer KeyEnter
// runs submit through its single code path.
func (m *Model) handlePopupKey(msg tea.KeyMsg) (tea.Cmd, bool) {
	matches := filterCommands(commands, m.input.Value())
	if len(matches) == 0 {
		return nil, false // defensive: popupShown implied >0 above
	}
	switch msg.Type {
	case tea.KeyUp, tea.KeyCtrlP:
		m.popupSel = (m.popupSel - 1 + len(matches)) % len(matches)
		return nil, true
	case tea.KeyDown, tea.KeyCtrlN:
		m.popupSel = (m.popupSel + 1) % len(matches)
		return nil, true
	case tea.KeyTab:
		m.acceptPopup(matches[m.popupSel])
		m.onInputChanged()
		return nil, true
	case tea.KeyEnter:
		m.acceptPopup(matches[m.popupSel])
		m.onInputChanged()
		// Fall through to outer KeyEnter so submit() handles dispatch.
		// Returning (nil, false) is the contract the caller looks at.
		return nil, false
	case tea.KeyEsc:
		// Esc collapses the popup by emptying input; the outer ESC chain
		// (abort / clear) only kicks in on the *next* Esc press, when
		// the popup is no longer in the way.
		m.input.SetValue("")
		m.onInputChanged()
		return nil, true
	}
	return nil, false
}

// acceptPopup replaces the entire input with "/<name>" and parks the
// cursor at end-of-line. No trailing space: zero-arg commands let the
// user hit Enter immediately, and arg-taking commands let the user
// type ' on' themselves — uniformity beats clever-spacing.
func (m *Model) acceptPopup(c slashCommand) {
	m.input.SetValue("/" + c.Name)
	m.input.CursorEnd()
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
		m.messages = freshMessages(m.width, m.modelName, m.cwd)
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
	case "todos":
		return m.handleTodosCmd(text), true
	case "help":
		m.pushMessage("user", text)
		m.pushMessage("assistant", builtinHelp())
		return nil, true
	}
	return nil, false
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
