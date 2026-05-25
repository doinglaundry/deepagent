package tui

import (
	"context"
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

	"eino-cli/backend/config"
	rt "eino-cli/backend/runtime"
	runtimeRun "eino-cli/backend/runtime/run"
	"eino-cli/backend/session/rollback"
	"eino-cli/backend/session/runs"
)

const (
	defaultToolPreviewLines = 5
	defaultToolArgsMaxChars = 60
)

type chatMessage struct {
	Role     string
	Content  string
	Rendered string
}

type Model struct {
	rt        rt.Runtime
	cfg       *config.Config
	sessionID string
	cwd       string
	modelName string
	runs      *runtimeRun.Manager

	input    textinput.Model
	viewport viewport.Model
	spin     spinner.Model

	messages  []chatMessage
	streaming bool
	streamBuf strings.Builder

	streamStart time.Time
	elapsed     time.Duration
	verbPresent string
	verbPast    string

	streamCh <-chan tea.Msg
	cancel   context.CancelFunc

	mdRenderer *glamour.TermRenderer
	mdStyle    string
	width      int
	height     int
	ready      bool

	pendingExit   bool
	popupSel      int
	commands      []slashCommand
	lastErr       error
	planMode      bool
	tokenTotal    int64
	shimmerOffset int

	toolBlocks        []*toolBlock
	toolBlocksEnabled bool
	lastSeenMsgCount  int
	toolBlockSeq      int
	toolPreviewLines  int
	toolArgsMaxChars  int
	footerHint        string
	flushedMsgCount   int
	pendingScrollback []string
	runHistoryOpen    bool
	runHistoryRows    []runs.Record
	runHistorySel     int
	hitlQueue         []approvalRequest
	todos             []deep.TODO
	todoExpanded      bool
	prog              *tea.Program
}

func New(runtime rt.Runtime, sessionID string, cfgs ...*config.Config) (*Model, error) {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}
	var cfg *config.Config
	if len(cfgs) > 0 {
		cfg = cfgs[0]
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

	style := "dark"
	if !lipgloss.HasDarkBackground() {
		style = "light"
	}

	return &Model{
		rt:        runtime,
		cfg:       cfg,
		sessionID: sessionID,
		cwd:       cwd,
		modelName: runtime.Name(),
		runs: runtimeRun.NewManagerWithStore(
			runs.NewStore(config.SessionRunsDir(sessionID)),
			rollback.NewStore(config.RootDir(), sessionID),
		),
		input:             ti,
		viewport:          vp,
		spin:              sp,
		messages:          freshMessages(0, runtime.Name(), cwd),
		mdStyle:           style,
		toolBlocksEnabled: true,
		toolPreviewLines:  defaultToolPreviewLines,
		toolArgsMaxChars:  defaultToolArgsMaxChars,
		commands:          buildSlashCommands(cfg),
	}, nil
}

func (m *Model) Init() tea.Cmd {
	return textinput.Blink
}

func getAvailableCommands(m *Model) []slashCommand {
	if len(m.commands) > 0 {
		return m.commands
	}
	commands := append([]slashCommand(nil), builtinCommands...)
	attachBuiltinHandlers(commands)
	return commands
}

func renderMarkdown(m *Model, content string) string {
	width := m.viewport.Width - 2
	if width <= 0 {
		width = 78
	}
	if m.mdRenderer == nil {
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
	out = strings.TrimSpace(out)
	return xansi.Wrap(out, width, " ")
}

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

func resetConversationUI(m *Model) {
	m.messages = freshMessages(m.width, m.modelName, m.cwd)
	m.toolBlocks = nil
	m.lastSeenMsgCount = 0
	m.toolBlockSeq = 0
	m.flushedMsgCount = 0
	m.pendingScrollback = nil
	m.todos = nil
}

func resetConversationView(m *Model) {
	resetConversationUI(m)
	m.footerHint = ""
	m.todoExpanded = false
	rebuildHistory(m)
}

func rebuildHistory(m *Model) {
	queueScrollback(m)
	live := getLiveMessages(m)
	if len(live) == 0 {
		m.viewport.SetContent("")
	} else {
		parts := make([]string, 0, len(live)*2)
		for _, msg := range live {
			parts = append(parts, renderMessage(m, msg))
		}
		m.viewport.SetContent(strings.Join(parts, "\n\n"))
	}
	recomputeLayout(m)
	m.viewport.GotoBottom()
}

func getLiveMessages(m *Model) []chatMessage {
	if len(m.messages) == 0 {
		return nil
	}
	if m.flushedMsgCount < 0 {
		m.flushedMsgCount = 0
	}
	if m.flushedMsgCount >= len(m.messages) {
		return nil
	}
	if len(m.messages) == 1 {
		if m.messages[0].Role == "banner" && m.flushedMsgCount >= 1 {
			return nil
		}
		return m.messages
	}
	return m.messages[m.flushedMsgCount:]
}

func queueScrollback(m *Model) {
	if len(m.messages) == 0 || m.streaming {
		return
	}
	if m.flushedMsgCount < 0 {
		m.flushedMsgCount = 0
	}
	if len(m.messages) == 1 {
		if m.flushedMsgCount == 0 {
			queueScrollbackMessage(m, m.messages[0])
			m.flushedMsgCount = 1
		}
		return
	}
	target := len(m.messages) - 1
	if m.messages[len(m.messages)-1].Role == "assistant" {
		for target > m.flushedMsgCount && m.messages[target-1].Role == "tool-block" {
			target--
		}
	}
	if target <= m.flushedMsgCount {
		return
	}
	for _, msg := range m.messages[m.flushedMsgCount:target] {
		queueScrollbackMessage(m, msg)
	}
	m.flushedMsgCount = target
}

func queueCompletedTurnScrollback(m *Model) {
	if m.flushedMsgCount < 0 {
		m.flushedMsgCount = 0
	}
	for _, msg := range m.messages[m.flushedMsgCount:] {
		queueScrollbackMessage(m, msg)
	}
	m.flushedMsgCount = len(m.messages)
	m.viewport.SetContent("")
	recomputeLayout(m)
}

func queueScrollbackMessage(m *Model, msg chatMessage) {
	if text := strings.TrimSpace(renderMessage(m, msg)); text != "" {
		m.pendingScrollback = append(m.pendingScrollback, text)
	}
	if msg.Role != "tool-block" {
		return
	}
	if id, ok := parseToolPlaceholder(msg.Content); ok {
		if block := getToolBlockByID(m, id); block != nil {
			block.flushed = true
		}
	}
}

func flushScrollbackCmd(m *Model) tea.Cmd {
	if len(m.pendingScrollback) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(m.pendingScrollback))
	for _, text := range m.pendingScrollback {
		cmds = append(cmds, tea.Println(text))
	}
	m.pendingScrollback = nil
	return tea.Batch(cmds...)
}

func renderMessage(m *Model, msg chatMessage) string {
	switch msg.Role {
	case "user":
		return userBlockStyle.Render("❯ " + msg.Content)
	case "assistant":
		body := msg.Rendered
		if body == "" {
			body = msg.Content
		}
		body = strings.ReplaceAll(body, "\n", "\n  ")
		return assistantPrefixStyle.Render("⏺ ") + body
	case "system":
		return systemPrefixStyle.Render("• ") + msg.Content
	case "tool-block":
		id, ok := parseToolPlaceholder(msg.Content)
		if !ok {
			return msg.Content
		}
		block := getToolBlockByID(m, id)
		if block == nil {
			return ""
		}
		return renderToolBlock(block, m.toolPreviewLines)
	case "thinking-summary":
		return thinkingSummaryStyle.Render("✻ " + msg.Content)
	case "banner":
		return msg.Content
	default:
		return msg.Content
	}
}

func pushMessage(m *Model, role, content string) {
	rendered := ""
	if role == "assistant" {
		rendered = renderMarkdown(m, content)
	}
	m.messages = append(m.messages, chatMessage{
		Role:     role,
		Content:  content,
		Rendered: rendered,
	})
	rebuildHistory(m)
}

func moveInputWord(m *Model, delta int) {
	value := m.input.Value()
	pos := m.input.Position()
	if delta < 0 {
		for pos > 0 && value[pos-1] == ' ' {
			pos--
		}
		for pos > 0 && value[pos-1] != ' ' {
			pos--
		}
	} else {
		for pos < len(value) && value[pos] == ' ' {
			pos++
		}
		for pos < len(value) && value[pos] != ' ' {
			pos++
		}
	}
	m.input.SetCursor(pos)
}

func abortStream(m *Model) bool {
	if !m.streaming {
		return false
	}
	if m.cancel != nil {
		m.cancel()
	}
	drainApprovals(m)
	return true
}
