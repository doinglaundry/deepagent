package tui

import "strings"

// slashCommand is the static metadata for one built-in slash command.
// Description and Args drive popup rendering only; dispatch still lives
// in update.go's handleBuiltin switch — registry-driven dispatch is a
// separate refactor and intentionally out of scope here.
type slashCommand struct {
	Name string // without the leading "/"
	Args string // e.g. "[on|off|toggle]"; empty when the command takes none
	Desc string // one short line; popup truncates to viewport width
}

// commands is the single source of truth for the slash-command popup.
// Order = display order; alphabetical keeps the empty-query menu
// predictable. Keep in sync with handleBuiltin in update.go — a name
// listed here but not dispatched there silently submits to the LLM as
// a plain prompt.
var commands = []slashCommand{
	{Name: "bootstrap", Desc: "create or update yaml/soul.md through onboarding"},
	{Name: "clear", Desc: "clear the in-memory conversation history"},
	{Name: "debug", Args: "[on|off|toggle]", Desc: "show / hide the model's exact input & output per turn"},
	{Name: "exit", Desc: "exit the TUI session"},
	{Name: "help", Desc: "show this help"},
	{Name: "quit", Desc: "exit the TUI session"},
	{Name: "todos", Args: "[open|close|toggle]", Desc: "expand / collapse the todo panel"},
}

// shouldShowPopup gates popup visibility on the input value alone. The
// rule: input must start with "/" AND have no whitespace yet (still in
// the command-name region). Once the user types a space we're in the
// argument region — the menu disappears so /plan, /debug etc. can take
// their on/off/toggle freely.
func shouldShowPopup(input string) bool {
	if !strings.HasPrefix(input, "/") {
		return false
	}
	return !strings.ContainsAny(input, " \t")
}

// filterCommands returns the entries whose Name starts with the slash-
// stripped query, case-insensitive. Empty query returns the full list
// so a bare "/" pops the whole menu (matches Claude Code's UX). The
// returned slice may alias the input on empty-query — callers must not
// mutate it.
func filterCommands(all []slashCommand, query string) []slashCommand {
	query = strings.ToLower(strings.TrimPrefix(query, "/"))
	if query == "" {
		return all
	}
	out := make([]slashCommand, 0, len(all))
	for _, c := range all {
		if strings.HasPrefix(strings.ToLower(c.Name), query) {
			out = append(out, c)
		}
	}
	return out
}
