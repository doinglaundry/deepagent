package tui

import (
	"fmt"
	"strings"
)

const popupMaxRows = 10

func renderPopup(m *Model) string {
	input := m.input.Value()
	if !shouldShowPopup(input) {
		return ""
	}
	matches := getPopupMatches(m)
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
		sel = 0
	}

	lines := make([]string, 0, len(visible)+1)
	for i, c := range visible {
		body := buildPopupRowBody(c)
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

func buildPopupRowBody(c slashCommand) string {
	name := popupNameStyle.Render("/" + c.Name)
	kind := popupArgsStyle.Render("[" + c.Type + "]")
	if c.Args == "" {
		return fmt.Sprintf("%s %s  %s", name, kind, popupDescStyle.Render("— "+c.Desc))
	}
	return fmt.Sprintf("%s %s %s  %s",
		name,
		popupArgsStyle.Render(c.Args),
		kind,
		popupDescStyle.Render("— "+c.Desc),
	)
}

func getPopupHeight(m *Model) int {
	if !shouldShowPopup(m.input.Value()) {
		return 0
	}
	matches := getPopupMatches(m)
	if len(matches) == 0 {
		return 0
	}
	if len(matches) > popupMaxRows {
		return popupMaxRows + 1
	}
	return len(matches)
}
