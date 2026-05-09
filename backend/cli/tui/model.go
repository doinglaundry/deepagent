package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/runtime/eino"
)

// Per-message body / tool-call argument caps used by the debug
// formatters. Sized so a typical 2-4 KB system prompt fits whole on
// the first turn but later turns don't push the input off-screen.
const (
	debugBodyMaxBytes    = 4 << 10
	debugToolArgMaxBytes = 1 << 10
)

// chatMessage is the TUI's view-side message record. It holds the
// rendered (markdown-formatted, for assistants) string so View can
// paste history into the viewport without re-rendering on every
// keystroke.
type chatMessage struct {
	Role     string // "user" | "assistant" | "system" | "debug-input" | "debug-output" | "banner"
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

	chunkCh <-chan string
	cancel  context.CancelFunc

	mdRenderer *glamour.TermRenderer
	// mdStyle is the glamour style name ("dark" / "light"), detected
	// once in New() before bubbletea takes over stdin. Caching it
	// here keeps subsequent renderer rebuilds (on resize) from
	// re-querying the terminal — those queries leak their OSC 11
	// responses into textinput once we're in raw mode.
	mdStyle string
	width   int
	height  int
	ready   bool

	// pendingExit is set by the first Ctrl-C in idle state; a
	// second Ctrl-C while it's set quits.
	pendingExit bool

	// lastErr surfaces the most recent runtime error in the
	// streaming panel until it's cleared by the next submit.
	lastErr error

	// debug toggles inline display of model input/output for each
	// LLM turn. Off by default; flipped via the /debug slash.
	debug bool

	// prog is the back-reference to the bubbletea Program, set by
	// Run() in tui.go just before prog.Run(). Used to cross-goroutine
	// Send debug events from the trace middleware.
	prog *tea.Program
}

// New builds a Model wired to the supplied runtime. Heavy
// dependencies (config.Load, eino.BuildRuntime) live in
// cmd/tui/main.go; this constructor is intentionally narrow so
// tests can substitute a fake Runtime.
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

	// Detect terminal background ONCE here, while stdin is still in
	// cooked mode. After Run() hands stdin to bubbletea (raw mode),
	// any OSC 11 query response would race with bubbletea's
	// keypress parser and leak into textinput as visible bytes.
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
		messages:  freshMessages(),
		mdStyle:   style,
	}, nil
}

func (m *Model) Init() tea.Cmd {
	// Spinner ticks are scheduled on demand from submit(); the
	// chain self-terminates in the spinner.TickMsg branch of
	// Update() once streaming flips false.
	return textinput.Blink
}

// renderMarkdown lazily builds (or rebuilds) the glamour renderer
// for the current viewport width and converts content to ANSI.
// On error it falls back to the raw text so the chat doesn't go
// silent because of a markdown parse hiccup.
func (m *Model) renderMarkdown(content string) string {
	width := m.viewport.Width
	if width <= 0 {
		width = 80
	}
	if m.mdRenderer == nil {
		// WithStandardStyle (not WithAutoStyle): the latter sends an
		// OSC 11 query to the terminal at every renderer rebuild,
		// and bubbletea's raw-mode input parser then misreads the
		// response as keypresses. We resolved dark/light once in
		// New() before bubbletea claimed stdin.
		r, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle(m.mdStyle),
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
	// Glamour wraps output in a document margin: leading newline +
	// per-line indent + trailing newline. Strip leading/trailing
	// whitespace so the "⏺ " prefix sits flush against the first
	// character on the same line; internal indentation between
	// paragraphs is preserved.
	return strings.TrimSpace(out)
}

// rebuildHistory regenerates the viewport's content string from
// m.messages. Called whenever messages change or the window
// resizes (markdown wraps to a new width).
func (m *Model) rebuildHistory() {
	if len(m.messages) == 0 {
		m.viewport.SetContent("")
		return
	}
	parts := make([]string, 0, len(m.messages)*2)
	for _, msg := range m.messages {
		parts = append(parts, m.renderMessage(msg))
	}
	m.viewport.SetContent(strings.Join(parts, "\n\n"))
	m.viewport.GotoBottom()
}

func (m *Model) renderMessage(msg chatMessage) string {
	switch msg.Role {
	case "user":
		return userPrefixStyle.Render("❯ ") + msg.Content
	case "assistant":
		body := msg.Rendered
		if body == "" {
			body = msg.Content
		}
		return assistantPrefixStyle.Render("⏺ ") + body
	case "system":
		return systemPrefixStyle.Render("• ") + msg.Content
	case "debug-input":
		return debugInputMarkerStyle.Render("▶ ") + debugBodyStyle.Render(msg.Content)
	case "debug-output":
		return debugOutputMarkerStyle.Render("◀ ") + debugBodyStyle.Render(msg.Content)
	case "banner":
		// Already pre-rendered ANSI (figlet block letters + dim
		// subtitle). Return verbatim — no prefix, no markdown.
		return msg.Content
	default:
		return msg.Content
	}
}

// pushMessage appends to history, pre-rendering markdown for
// assistant messages so View doesn't pay the cost on each key
// stroke.
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

// abortStream cancels an in-flight runtime call, if any. Returns
// true when there was an active stream to cancel.
func (m *Model) abortStream() bool {
	if !m.streaming {
		return false
	}
	if m.cancel != nil {
		m.cancel()
	}
	return true
}

// builtinHelp returns the static /help body.
func builtinHelp() string {
	return strings.TrimSpace(fmt.Sprintf(`
**Built-in commands**
- %s — clear the in-memory conversation history
- %s — show / hide the model's exact input & output per turn
- %s — exit the TUI session
- %s — exit the TUI session
- %s — show this help

Anything else is sent to the model as a prompt. Press Ctrl-C
during a response to abort, or Ctrl-C twice from idle to quit.
`, "`/clear`", "`/debug [on|off|toggle]`", "`/exit`", "`/quit`", "`/help`"))
}

// formatDebugInput renders a DebugBefore event into the plain-text
// body that goes into a "debug-input" chatMessage. The format is
// human-skim-friendly (per-message lines, optional tool_call sub-
// lines) and bounded by debugBodyMaxBytes to keep one turn's snapshot
// within roughly a screen.
//
// The [agentname] prefix on the header line keeps subagent events
// distinguishable when they interleave with the lead agent's on the
// same consumer (each agent has its own Trace + independent turn
// counter, so without the prefix the user would see confusing
// "two turn 1 events" sequences).
func formatDebugInput(ev middlewares.DebugEvent) string {
	var sb strings.Builder
	var totalBytes int
	for _, msg := range ev.Messages {
		totalBytes += len(msg.Content)
	}
	fmt.Fprintf(&sb, "[%s] turn %d input · %d messages · %s\n",
		ev.AgentName, ev.Turn, len(ev.Messages), humanBytes(totalBytes))
	for _, msg := range ev.Messages {
		fmt.Fprintf(&sb, "[%s] %s\n", msg.Role, truncate(msg.Content, debugBodyMaxBytes))
		for _, tc := range msg.ToolCalls {
			fmt.Fprintf(&sb, "  ↳ tool_call %s(%s)\n",
				tc.Function.Name,
				truncate(tc.Function.Arguments, debugToolArgMaxBytes))
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// formatDebugOutput renders a DebugAfter event. By contract the event
// carries exactly one message — the model's just-returned assistant
// message — so we only ever print one body line plus its tool calls.
func formatDebugOutput(ev middlewares.DebugEvent) string {
	if len(ev.Messages) == 0 {
		return ""
	}
	last := ev.Messages[0]
	var sb strings.Builder
	fmt.Fprintf(&sb, "[%s] turn %d output\n", ev.AgentName, ev.Turn)
	if c := strings.TrimSpace(last.Content); c != "" {
		fmt.Fprintf(&sb, "[%s] %s\n", last.Role, truncate(last.Content, debugBodyMaxBytes))
	}
	for _, tc := range last.ToolCalls {
		fmt.Fprintf(&sb, "  ↳ tool_call %s(%s)\n",
			tc.Function.Name,
			truncate(tc.Function.Arguments, debugToolArgMaxBytes))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// truncate clips s to at most n bytes (UTF-8 boundary–unaware: matches
// the existing project convention of byte-counting for prompt budgets).
// When clipped, it appends a "(…N more bytes)" tail so the reader knows
// the snapshot was abridged.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf(" …(%d more bytes)", len(s)-n)
}

// humanBytes formats a byte count as a short string ("1.2 KB", "456 B").
// Only used for debug headers, so we don't need MB+.
func humanBytes(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}
