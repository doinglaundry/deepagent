// Package tui is the Bubbletea chat front-end wrapping eino.Runtime.
package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"eino-cli/backend/runtime/eino"
)

// Run starts the alt-screen TUI bound to the inherited TTY; bypasses bubbletea's
// /dev/tty fallback (broken in IDE terminals / sandboxed subprocesses / nohup).
func Run(rt eino.Runtime) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("stdin is not a terminal: eino-tui needs an interactive TTY (try running it directly, not piped or backgrounded)")
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return fmt.Errorf("stdout is not a terminal: eino-tui needs an interactive TTY (try running it directly, not piped or redirected)")
	}

	m, err := New(rt)
	if err != nil {
		return err
	}
	// Mouse intentionally off: stray bytes from incomplete SGR sequences leak
	// into textinput as visible characters, and we don't need clicks anyway.
	prog := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithInput(os.Stdin),
		tea.WithOutput(os.Stdout),
	)
	m.prog = prog
	_, err = prog.Run()
	return err
}
