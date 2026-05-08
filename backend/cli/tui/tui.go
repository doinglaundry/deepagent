// Package tui implements a Bubbletea-based interactive chat
// front-end for eino-cli. It wraps the existing eino.Runtime with
// a Charm TUI: header, scrollable history, live-streaming text
// panel, single-line input, and a footer.
//
// See specs/20260506-tui-cli/plan.md for the helixent-port
// design rationale and the v1/v2 scope split.
package tui

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"

	"eino-cli/backend/runtime/eino"
)

// Run is the package-level entry point. It builds a Model around
// rt and hands it to a tea.Program in alt-screen mode (so the
// TUI doesn't trash the user's scrollback). Blocks until the user
// quits.
//
// We sanity-check that stdin/stdout are real TTYs up front. By
// default Bubbletea additionally tries to open /dev/tty as an
// input source (for the "piped stdin + live keyboard" use case);
// in some hosts (IDE integrated terminals, sandboxed subprocesses,
// containers without -it, nohup) /dev/tty isn't usable and the
// program errors with "open /dev/tty: device not configured".
// We bypass that fallback by binding os.Stdin / os.Stdout
// explicitly, which uses the inherited TTY directly.
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
	prog := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
		tea.WithInput(os.Stdin),
		tea.WithOutput(os.Stdout),
	)
	_, err = prog.Run()
	return err
}
