package tui

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"eino-cli/backend/config"
)

// slashCommand is the static metadata for one built-in slash command.
// Description and Args drive popup rendering only; dispatch still lives
// in update.go's handleBuiltin switch — registry-driven dispatch is a
// separate refactor and intentionally out of scope here.
type slashCommand struct {
	Name string // without the leading "/"
	Args string // e.g. "[on|off|toggle]"; empty when the command takes none
	Desc string // one short line; popup truncates to viewport width
	Type string // "builtin" or "skill"
}

// commands is the single source of truth for the slash-command popup.
// Order = display order; alphabetical keeps the empty-query menu
// predictable. Keep in sync with handleBuiltin in update.go — a name
// listed here but not dispatched there silently submits to the LLM as
// a plain prompt.
var builtinCommands = []slashCommand{
	{Name: "bootstrap", Desc: "create or update yaml/soul.md through onboarding", Type: "builtin"},
	{Name: "clear", Desc: "clear the in-memory conversation history", Type: "builtin"},
	{Name: "debug", Args: "[on|off|toggle]", Desc: "show / hide the model's exact input & output per turn", Type: "builtin"},
	{Name: "exit", Desc: "exit the TUI session", Type: "builtin"},
	{Name: "help", Args: "[name]", Desc: "show slash commands, or details for one command", Type: "builtin"},
	{Name: "quit", Desc: "exit the TUI session", Type: "builtin"},
	{Name: "reload", Desc: "restart the agent service", Type: "builtin"},
	{Name: "todos", Args: "[open|close|toggle]", Desc: "expand / collapse the todo panel", Type: "builtin"},
}

var commands = builtinCommands

func buildSlashCommands(cfg *config.Config) []slashCommand {
	commands := append([]slashCommand(nil), builtinCommands...)
	seen := make(map[string]bool, len(commands))
	for _, command := range commands {
		seen[strings.ToLower(command.Name)] = true
	}
	for _, command := range loadSkillSlashCommands(cfg) {
		key := strings.ToLower(command.Name)
		if seen[key] {
			continue
		}
		seen[key] = true
		commands = append(commands, command)
	}
	sort.SliceStable(commands, func(i, j int) bool {
		if commands[i].Type != commands[j].Type {
			return commands[i].Type == "builtin"
		}
		return commands[i].Name < commands[j].Name
	})
	return commands
}

func loadSkillSlashCommands(cfg *config.Config) []slashCommand {
	if cfg == nil {
		return nil
	}
	var commands []slashCommand
	for _, root := range cfg.Skills.Paths {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || d.Name() != "SKILL.md" {
				return nil
			}
			if command, ok := parseSkillCommand(path); ok {
				commands = append(commands, command)
			}
			return nil
		})
	}
	return commands
}

func parseSkillCommand(path string) (slashCommand, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return slashCommand{}, false
	}
	name := filepath.Base(filepath.Dir(path))
	description := ""
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) == "---" {
		for _, line := range lines[1:] {
			line = strings.TrimSpace(line)
			if line == "---" {
				break
			}
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			value = strings.Trim(strings.TrimSpace(value), `"'`)
			switch strings.TrimSpace(key) {
			case "name":
				if value != "" {
					name = value
				}
			case "description":
				description = value
			}
		}
	}
	if strings.TrimSpace(name) == "" {
		return slashCommand{}, false
	}
	if description == "" {
		description = "run skill " + name
	}
	return slashCommand{Name: name, Desc: description, Type: "skill"}, true
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
		name := strings.ToLower(c.Name)
		desc := strings.ToLower(c.Desc)
		if strings.Contains(name, query) || (len(query) >= 3 && strings.Contains(desc, query)) {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return commandScore(out[i], query) > commandScore(out[j], query)
	})
	return out
}

func commandScore(command slashCommand, query string) int {
	name := strings.ToLower(command.Name)
	desc := strings.ToLower(command.Desc)
	switch {
	case strings.HasPrefix(name, query):
		return 3
	case strings.Contains(name, query):
		return 2
	case len(query) >= 3 && strings.Contains(desc, query):
		return 1
	default:
		return 0
	}
}

func findCommand(commands []slashCommand, name string) (slashCommand, bool) {
	name = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(name), "/"))
	for _, command := range commands {
		if strings.ToLower(command.Name) == name {
			return command, true
		}
	}
	return slashCommand{}, false
}

func highlightedCommandName(text string, commands []slashCommand) string {
	if !strings.HasPrefix(text, "/") {
		return ""
	}
	token, _, ok := strings.Cut(text, " ")
	if !ok {
		return ""
	}
	name := strings.TrimPrefix(token, "/")
	if _, found := findCommand(commands, name); !found {
		return ""
	}
	return name
}
