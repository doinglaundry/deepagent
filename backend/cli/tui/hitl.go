package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"eino-cli/backend/agent"
)

type approvalRequest struct {
	toolName string
	args     string
	reply    chan bool
}

const approvalPromptHeight = 3

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

var (
	approvalHeaderStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214"))
	approvalArgsStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	approvalHintStyle   = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("241"))
)
