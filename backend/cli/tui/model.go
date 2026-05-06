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

	"eino-cli/backend/runtime/eino"
)

// chatMessage is the TUI's view-side message record. It holds the
// rendered (markdown-formatted, for assistants) string so View can
// paste history into the viewport without re-rendering on every
// keystroke.
type chatMessage struct {
	Role     string // "user" | "assistant" | "system"
	Content  string // raw text
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
	width      int
	height     int
	ready      bool

	// pendingExit is set by the first Ctrl-C in idle state; a
	// second Ctrl-C while it's set quits.
	pendingExit bool

	// lastErr surfaces the most recent runtime error in the
	// streaming panel until it's cleared by the next submit.
	lastErr error
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

	return &Model{
		rt:        rt,
		cwd:       cwd,
		modelName: rt.Name(),
		input:     ti,
		viewport:  vp,
		spin:      sp,
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
		r, err := glamour.NewTermRenderer(
			glamour.WithAutoStyle(),
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
	return strings.TrimRight(out, "\n")
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
- %s — exit the TUI session
- %s — exit the TUI session
- %s — show this help

Anything else is sent to the model as a prompt. Press Ctrl-C
during a response to abort, or Ctrl-C twice from idle to quit.
`, "`/clear`", "`/exit`", "`/quit`", "`/help`"))
}
