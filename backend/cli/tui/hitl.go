package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"eino-cli/backend/agent"
)

// approvalRequest is the tea.Msg variant emitted by the agent goroutine
// when a HITL-gated tool call needs a yes/no decision. The reply chan
// is single-shot: the keypress handler sends one bool then leaves it,
// and the approver goroutine receives the bool (or sees ctx
// cancellation, whichever wins) and returns. We never re-use the
// channel — one request, one reply, GC takes care of the rest.
type approvalRequest struct {
	toolName string
	args     string
	reply    chan bool
}

// approvalPromptHeight is the fixed line count of renderApprovalPrompt.
// recomputeLayout reserves exactly this many cells when m.hitlQueue is
// non-empty; the renderer guarantees three lines (title / args / hint)
// by truncating long args to a single screen line. Drift between the
// two would silently misalign chrome.
const approvalPromptHeight = 3

// installTUIApproval rewires agent.HITLApprover so HITL prompts render
// inside the TUI surface — alt-screen-friendly — instead of the default
// stdin scanner that would deadlock against bubbletea owning stdin.
//
// Call this ONCE during Run(), after tea.NewProgram but before
// prog.Run() yields control. The closure captures prog so every agent
// goroutine — lead agent, every subagent — funnels into the same
// tea.Program.Send queue.
//
// If ctx is cancelled while the user is mid-prompt the approver
// returns false (deny is the safe default). Stale entries in
// m.hitlQueue are drained when the stream finishes; nobody else needs
// to read the orphaned reply channel.
func installTUIApproval(prog *tea.Program) func() {
	previous := agent.HITLApprover
	agent.HITLApprover = func(ctx context.Context, toolName, args string) bool {
		reply := make(chan bool, 1)
		prog.Send(approvalRequest{
			toolName: toolName,
			args:     args,
			reply:    reply,
		})
		select {
		case decision := <-reply:
			return decision
		case <-ctx.Done():
			return false
		}
	}
	return func() {
		agent.HITLApprover = previous
	}
}

// renderApprovalPrompt draws the panel that sits above the input box
// while the front of m.hitlQueue is awaiting a decision. The contract
// with recomputeLayout is approvalPromptHeight lines (currently 3); if
// you add a line here, bump that constant in lockstep.
func renderApprovalPrompt(req approvalRequest, width int) string {
	const argLimit = 200
	args := req.args
	if width > 0 && len(args) > width {
		args = args[:max(0, width-12)] + " …"
	} else if len(args) > argLimit {
		args = args[:argLimit] + fmt.Sprintf(" …(%d more bytes)", len(req.args)-argLimit)
	}
	header := approvalHeaderStyle.Render(fmt.Sprintf("● Approve tool %q?", req.toolName))
	argsLine := approvalArgsStyle.Render(args)
	hint := approvalHintStyle.Render("y = allow · n / Enter / Esc = deny · Ctrl-C = deny + abort")
	return strings.Join([]string{header, argsLine, hint}, "\n")
}

// approvalHeaderStyle / approvalArgsStyle / approvalHintStyle live here
// rather than in styles.go because they're the only consumer; keeping
// them next to renderApprovalPrompt keeps the visual contract local.
var (
	approvalHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")) // orange (matches systemPrefix)
	approvalArgsStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	approvalHintStyle   = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("241"))
)
