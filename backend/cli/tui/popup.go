package tui

import (
	"fmt"
	"strings"
)

// popupMaxRows caps visible rows. Current pool is 7; 8 leaves headroom
// without pagination — bump this if the registry grows, then revisit
// scrolling.
const popupMaxRows = 8

// renderPopup returns the slash-command candidate menu, or "" when the
// popup is hidden (no '/', or arg region, or zero matches). Callers can
// concat unconditionally.
//
// IMPORTANT: line count must stay in lockstep with popupHeight — the
// layout subtracts popupHeight from the viewport budget, and any drift
// shoves the input box off-screen.
func (m *Model) renderPopup() string {
	input := m.input.Value()
	if !shouldShowPopup(input) {
		return ""
	}
	matches := filterCommands(commands, input)
	if len(matches) == 0 {
		return ""
	}

	visible := matches
	overflow := 0
	if len(visible) > popupMaxRows {
		overflow = len(visible) - popupMaxRows
		visible = visible[:popupMaxRows]
	}

	sel := m.popupSel
	if sel < 0 || sel >= len(visible) {
		sel = 0 // defensive: handleKey clamps too, but render must not panic
	}

	lines := make([]string, 0, len(visible)+1)
	for i, c := range visible {
		body := popupRowBody(c)
		if i == sel {
			lines = append(lines, popupSelectedRow.Render(body))
		} else {
			lines = append(lines, popupRowStyle.Render(body))
		}
	}
	if overflow > 0 {
		lines = append(lines, popupRowStyle.Render(popupDescStyle.Render(
			fmt.Sprintf("… (+%d more)", overflow))))
	}
	return strings.Join(lines, "\n")
}

// popupRowBody renders one row sans row-level background. Format:
//
//	/<name> [args]  — <desc>
//
// Args is dropped when empty so /clear reads as "/clear  — clear …"
// instead of carrying a hanging gap.
func popupRowBody(c slashCommand) string {
	name := popupNameStyle.Render("/" + c.Name)
	if c.Args == "" {
		return fmt.Sprintf("%s  %s", name, popupDescStyle.Render("— "+c.Desc))
	}
	return fmt.Sprintf("%s %s  %s",
		name,
		popupArgsStyle.Render(c.Args),
		popupDescStyle.Render("— "+c.Desc),
	)
}

// popupHeight returns the line count renderPopup will emit; consumed by
// recomputeLayout. Must match renderPopup line-for-line.
func (m *Model) popupHeight() int {
	if !shouldShowPopup(m.input.Value()) {
		return 0
	}
	matches := filterCommands(commands, m.input.Value())
	if len(matches) == 0 {
		return 0
	}
	if len(matches) > popupMaxRows {
		return popupMaxRows + 1 // visible rows + "+N more" tail
	}
	return len(matches)
}
