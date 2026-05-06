// Package tui implements a Bubbletea-based interactive chat
// front-end for eino-cli. It wraps the existing eino.Runtime with
// a Charm TUI: header, scrollable history, live-streaming text
// panel, single-line input, and a footer.
//
// See specs/20260506-tui-cli/plan.md for the helixent-port
// design rationale and the v1/v2 scope split.
package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"eino-cli/backend/runtime/eino"
)

// Run is the package-level entry point. It builds a Model around
// rt and hands it to a tea.Program in alt-screen mode (so the
// TUI doesn't trash the user's scrollback). Blocks until the user
// quits.
func Run(rt eino.Runtime) error {
	m, err := New(rt)
	if err != nil {
		return err
	}
	prog := tea.NewProgram(m,
		tea.WithAltScreen(),
		tea.WithMouseCellMotion(),
	)
	_, err = prog.Run()
	return err
}
