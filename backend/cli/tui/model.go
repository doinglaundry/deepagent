package tui

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	xansi "github.com/charmbracelet/x/ansi"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/cloudwego/eino/adk/prebuilt/deep"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/runtime/eino"
)

// Per-message / tool-arg caps for the debug panel; sized so a 2-4 KB
// system prompt fits on first turn but later turns stay on-screen.
const (
	debugBodyMaxBytes    = 4 << 10
	debugToolArgMaxBytes = 1 << 10
)

// chatMessage caches the markdown-rendered body so View doesn't re-render per keystroke.
type chatMessage struct {
	Role     string // "user" | "assistant" | "system" | "debug-input" | "debug-output" | "thinking-summary" | "banner"
	Content  string // raw text (or pre-rendered ANSI for "banner")
	Rendered string // post-markdown, for assistant only
}

// Model is the bubbletea single-source-of-truth.
type Model struct {
	rt        eino.Runtime
	cwd       string
	modelName string

	input    textinput.Model
	viewport viewport.Model
	spin     spinner.Model

	messages  []chatMessage
	streaming bool
	streamBuf strings.Builder

	// Thinking-indicator state for the active streaming turn.
	// streamStart anchors elapsed; verbPresent / verbPast are picked
	// once per submit() so the live indicator ("Moonwalking…") and the
	// completion summary ("Moonwalked for 6s") share the same verb.
	// All four are zeroed when handleDone fires.
	streamStart time.Time
	elapsed     time.Duration
	verbPresent string
	verbPast    string

	chunkCh <-chan string
	cancel  context.CancelFunc

	mdRenderer *glamour.TermRenderer
	// mdStyle is detected once before bubbletea claims stdin; re-querying after
	// raw-mode would leak the OSC 11 response into textinput.
	mdStyle string
	width   int
	height  int
	ready   bool

	// pendingExit: first Ctrl-C in idle arms it, second Ctrl-C quits.
	pendingExit bool

	// popupSel is the selected row inside the currently-visible slash-
	// command candidate set. Visibility itself is derived from
	// m.input.Value() each render (see shouldShowPopup / filterCommands)
	// to avoid a second source of truth. Reset / clamped by
	// onInputChanged on any input edit.
	popupSel int

	lastErr error

	// debug toggles inline LLM input/output panels via /debug.
	debug bool

	// hitlQueue holds pending HITL approval requests in FIFO order;
	// hitlQueue[0] is what the prompt renders. The agent goroutine
	// blocks on each request's reply channel until handleKey picks a
	// y/n decision (or handleDone drains the queue when the stream
	// aborts). Multiple parallel subagents can queue concurrent
	// requests — bubbletea serialises Update calls so the queue's
	// only writer is Update itself.
	hitlQueue []approvalRequest

	// todos is the latest in-flight todo list, written by every
	// TracePhaseTodos event regardless of m.debug. Empty → no panel.
	todos []deep.TODO
	// todoExpanded toggles the panel between collapsed (single-line) and
	// expanded (full list) layouts; flipped by /todos.
	todoExpanded bool

	// prog back-reference lets cross-goroutine consumers (Trace middleware)
	// call prog.Send; wired in Run() right before prog.Run().
	prog *tea.Program
}

// New builds a Model around rt; heavy wiring (config/runtime) stays in main
// so tests can substitute a fake Runtime.
func New(rt eino.Runtime) (*Model, error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	ti := textinput.New()
	ti.Placeholder = "Ask anything... (/help for commands)"
	ti.Prompt = "❯ "
	ti.CharLimit = 0
	ti.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = primaryStyle

	vp := viewport.New(80, 10)

	// Detect background ONCE in cooked mode; raw-mode queries leak OSC 11 bytes.
	style := "dark"
	if !lipgloss.HasDarkBackground() {
		style = "light"
	}

	return &Model{
		rt:        rt,
		cwd:       cwd,
		modelName: rt.Name(),
		input:     ti,
		viewport:  vp,
		spin:      sp,
		messages:  freshMessages(0, rt.Name(), cwd),
		mdStyle:   style,
	}, nil
}

func (m *Model) Init() tea.Cmd {
	// Spinner ticks are scheduled on demand from submit().
	return textinput.Blink
}

// renderMarkdown lazily builds the glamour renderer; falls back to raw on error.
// Width reserves 2 cells for the "⏺ " prefix that renderMessage adds
// outside glamour's awareness — otherwise wrapped continuation lines
// (which renderMessage indents by 2) would overflow viewport.Width.
func (m *Model) renderMarkdown(content string) string {
	width := m.viewport.Width - 2
	if width <= 0 {
		width = 80
	}
	if m.mdRenderer == nil {
		// Custom style with Document.Margin zeroed: glamour's stock
		// dark/light styles indent every paragraph by 2 cells, which
		// pushed our "⏺ " prefix three cells away from its body. With
		// margin = 0 the body sits flush against the prefix, matching
		// the "❯ " user-line spacing.
		r, err := glamour.NewTermRenderer(
			glamour.WithStyles(noMarginStyle(m.mdStyle)),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			return content
		}
		m.mdRenderer = r
	}
	out, err := m.mdRenderer.Render(content)
	if err != nil {
		return content
	}
	// Glamour still emits document-level leading/trailing newlines even
	// with margin=0; trim them so the prefix lands on the first body line.
	out = strings.TrimSpace(out)
	// Glamour delegates wrapping to muesli/reflow, which splits on ASCII
	// whitespace. CJK runs have no spaces, so a paragraph of Chinese stays
	// as one long line and the viewport truncates it at MaxWidth (no soft
	// wrap on overflow). Re-wrap with an ANSI-aware grapheme wrapper that
	// hard-breaks long runs — width matches the renderer's WordWrap budget.
	return xansi.Wrap(out, width, " ")
}

// noMarginStyle clones glamour's standard "dark" / "light" style with
// Document.Margin forced to zero. Anything outside that pair falls back
// to ASCIIStyleConfig (mirrors glamour's getDefaultStyle resolution).
func noMarginStyle(name string) ansi.StyleConfig {
	var cfg ansi.StyleConfig
	switch name {
	case "light":
		cfg = styles.LightStyleConfig
	case "dark", "auto", "":
		cfg = styles.DarkStyleConfig
	default:
		cfg = styles.ASCIIStyleConfig
	}
	zero := uint(0)
	cfg.Document.Margin = &zero
	return cfg
}

// rebuildHistory regenerates the viewport content from m.messages.
// Content changes also retrigger recomputeLayout so the viewport's height
// shrinks/grows to match — without this the input would stay glued to
// the screen bottom even when there's only a banner on screen.
func (m *Model) rebuildHistory() {
	if len(m.messages) == 0 {
		m.viewport.SetContent("")
	} else {
		parts := make([]string, 0, len(m.messages)*2)
		for _, msg := range m.messages {
			parts = append(parts, m.renderMessage(msg))
		}
		m.viewport.SetContent(strings.Join(parts, "\n\n"))
	}
	m.recomputeLayout()
	m.viewport.GotoBottom()
}

func (m *Model) renderMessage(msg chatMessage) string {
	switch msg.Role {
	case "user":
		// Render prefix + content as a single styled span. Nesting
		// userPrefixStyle.Render(...) inside emits a reset (ESC[0m)
		// after the prefix which clears the outer Background — that's
		// why the "shadow" was invisible on the previous attempt. One
		// Render call → one Background span that survives across the
		// whole logical line.
		return userBlockStyle.Render("❯ " + msg.Content)
	case "assistant":
		body := msg.Rendered
		if body == "" {
			body = msg.Content
		}
		// Indent continuation lines so they sit under the "⏺ " prefix.
		// glamour word-wraps but the wrapped lines start at column 0;
		// without this, the second line visually leaves the message
		// block.
		body = strings.ReplaceAll(body, "\n", "\n  ")
		return assistantPrefixStyle.Render("⏺ ") + body
	case "system":
		return systemPrefixStyle.Render("• ") + msg.Content
	case "debug-input":
		return debugInputMarkerStyle.Render("▶ ") + debugBodyStyle.Render(msg.Content)
	case "debug-output":
		return debugOutputMarkerStyle.Render("◀ ") + debugBodyStyle.Render(msg.Content)
	case "thinking-summary":
		// One-line scrollback artefact: "✻ Verbed for Ns". No bold,
		// no indent — the dim magenta keeps it adjacent-but-quiet.
		return thinkingSummaryStyle.Render("✻ " + msg.Content)
	case "banner":
		// Pre-rendered ANSI; no prefix, no markdown.
		return msg.Content
	default:
		return msg.Content
	}
}

// pushMessage appends to history (pre-renders markdown for assistant).
func (m *Model) pushMessage(role, content string) {
	rendered := ""
	if role == "assistant" {
		rendered = m.renderMarkdown(content)
	}
	m.messages = append(m.messages, chatMessage{
		Role:     role,
		Content:  content,
		Rendered: rendered,
	})
	m.rebuildHistory()
}

// abortStream cancels any in-flight runtime call; returns true if one was active.
// Drains m.hitlQueue too: cancelling ctx is what drives the approver's
// ctx-done branch, but the visual queue still has stale rows; drop them
// here so the prompt panel disappears in the same frame.
func (m *Model) abortStream() bool {
	if !m.streaming {
		return false
	}
	if m.cancel != nil {
		m.cancel()
	}
	m.drainApprovals()
	return true
}

// builtinHelp returns the static /help body.
func builtinHelp() string {
	return strings.TrimSpace(fmt.Sprintf(`
**Built-in commands**
- %s — clear the in-memory conversation history
- %s — show / hide the model's exact input & output per turn
- %s — expand / collapse the todo panel
- %s — exit the TUI session
- %s — exit the TUI session
- %s — show this help

Anything else is sent to the model as a prompt. Press Ctrl-C
during a response to abort, or Ctrl-C twice from idle to quit.
`, "`/clear`", "`/debug [on|off|toggle]`", "`/todos [open|close|toggle]`", "`/exit`", "`/quit`", "`/help`"))
}

// formatDebugInput renders a TracePhaseBefore event; [agentname] prefix
// disambiguates interleaved subagent / lead-agent turns.
func formatDebugInput(ev middlewares.TraceEvent) string {
	var sb strings.Builder
	var totalBytes int
	for _, msg := range ev.Messages {
		totalBytes += len(msg.Content)
	}
	fmt.Fprintf(&sb, "[%s] turn %d input · %d messages · %s\n",
		ev.AgentName, ev.Turn, len(ev.Messages), humanBytes(totalBytes))
	for _, msg := range ev.Messages {
		fmt.Fprintf(&sb, "[%s] %s\n", msg.Role, truncate(msg.Content, debugBodyMaxBytes))
		for _, call := range msg.ToolCalls {
			fmt.Fprintf(&sb, "  ↳ tool_call %s(%s)\n",
				call.Function.Name,
				truncate(call.Function.Arguments, debugToolArgMaxBytes))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// formatDebugOutput renders a TracePhaseAfter event (one assistant message + tool calls).
func formatDebugOutput(ev middlewares.TraceEvent) string {
	if len(ev.Messages) == 0 {
		return ""
	}
	last := ev.Messages[0]
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s] turn %d output\n", ev.AgentName, ev.Turn)
	if c := strings.TrimSpace(last.Content); c != "" {
		fmt.Fprintf(&sb, "[%s] %s\n", last.Role, truncate(last.Content, debugBodyMaxBytes))
	}
	for _, call := range last.ToolCalls {
		fmt.Fprintf(&sb, "  ↳ tool_call %s(%s)\n",
			call.Function.Name,
			truncate(call.Function.Arguments, debugToolArgMaxBytes))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// truncate clips to n bytes (UTF-8 boundary unaware) with a "(…N more)" tail.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf(" …(%d more bytes)", len(s)-n)
}

// humanBytes formats bytes as "1.2 KB" / "456 B" for debug headers.
func humanBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}
