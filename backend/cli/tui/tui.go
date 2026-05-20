// Package tui is the Bubbletea chat front-end wrapping runtime.Runtime.
package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"eino-cli/backend/config"
	rt "eino-cli/backend/runtime"
)

// Run starts the alt-screen TUI bound to the inherited TTY; bypasses bubbletea's
// /dev/tty fallback (broken in IDE terminals / sandboxed subprocesses / nohup).
func Run(runtime rt.Runtime, cfgs ...*config.Config) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("stdin is not a terminal: eino-tui needs an interactive TTY (try running it directly, not piped or backgrounded)")
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("stdout is not a terminal: eino-tui needs an interactive TTY (try running it directly, not piped or redirected)")
	}

	m, err := New(runtime, cfgs...)
	if err != nil {
		return err
	}
	prog := tea.NewProgram(m,
		tea.WithInput(os.Stdin),
		tea.WithOutput(os.Stdout),
	)
	m.prog = prog
	// Route HITL approvals through this prog before any agent runs; restore
	// the previous approver when the TUI exits so later in-process runs do
	// not send approvals to a stopped tea.Program.
	restoreApproval := installTUIApproval(prog)
	defer restoreApproval()
	_, err = prog.Run()
	return err
}
