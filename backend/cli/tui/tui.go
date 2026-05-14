// Package tui is the Bubbletea chat front-end wrapping eino.Runtime.
package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"eino-cli/backend/config"
	"eino-cli/backend/runtime/eino"
)

// Run starts the alt-screen TUI bound to the inherited TTY; bypasses bubbletea's
// /dev/tty fallback (broken in IDE terminals / sandboxed subprocesses / nohup).
func Run(rt eino.Runtime, cfgs ...*config.Config) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("stdin is not a terminal: eino-tui needs an interactive TTY (try running it directly, not piped or backgrounded)")
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("stdout is not a terminal: eino-tui needs an interactive TTY (try running it directly, not piped or redirected)")
	}

	m, err := New(rt, cfgs...)
	if err != nil {
		return err
	}
	prog := tea.NewProgram(m,
		tea.WithInput(os.Stdin),
		tea.WithOutput(os.Stdout),
	)
	m.prog = prog
	// Route HITL approvals through this prog before any agent runs;
	// the default stdin scanner would deadlock against bubbletea's
	// alt-screen owning stdin/stdout. Safe to leave installed for the
	// process lifetime — eino-tui owns the terminal until prog.Run
	// returns and the process exits.
	installTUIApproval(prog)
	_, err = prog.Run()
	return err
}
