package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"eino-cli/backend/config"
)

type slashCommandHandler func(*Model, string) tea.Cmd

// slashCommand is the metadata and optional builtin handler for one slash command.
type slashCommand struct {
	Name    string // without the leading "/"
	Args    string // e.g. "[on|off|toggle]"; empty when the command takes none
	Desc    string // one short line; popup truncates to viewport width
	Type    string // "builtin" or "skill"
	Handler slashCommandHandler
}

var builtinCommands = []slashCommand{
	{Name: "clear", Desc: "clear the in-memory conversation history", Type: "builtin"},
	{Name: "dream", Desc: "consolidate transcript history into dream memory", Type: "builtin"},
	{Name: "exit", Desc: "exit the TUI session", Type: "builtin"},
	{Name: "help", Args: "[name]", Desc: "show slash commands, or details for one command", Type: "builtin"},
	{Name: "history", Desc: "browse past runs and rollback", Type: "builtin"},
	{Name: "plan", Args: "[on|off|toggle]", Desc: "inject plan-mode preamble into every model turn", Type: "builtin"},
	{Name: "quit", Desc: "exit the TUI session", Type: "builtin"},
	{Name: "todos", Args: "[open|close|toggle]", Desc: "expand / collapse the todo panel", Type: "builtin"},
}

var commands = builtinCommands

func buildSlashCommands(cfg *config.Config) []slashCommand {
	commands := append([]slashCommand(nil), builtinCommands...)
	seen := make(map[string]bool, len(commands))
	for _, command := range commands {
		seen[strings.ToLower(command.Name)] = true
	}
	for _, command := range loadSkillSlashCommands() {
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
	attachBuiltinHandlers(commands)
	return commands
}

func attachBuiltinHandlers(commands []slashCommand) {
	for i := range commands {
		if commands[i].Type != "builtin" {
			continue
		}
		switch commands[i].Name {
		case "clear":
			commands[i].Handler = handleClearCommand
		case "dream":
			commands[i].Handler = handleDreamCommand
		case "exit", "quit":
			commands[i].Handler = handleExitCommand
		case "help":
			commands[i].Handler = handleHelpCommand
		case "history":
			commands[i].Handler = handleHistoryCommand
		case "plan":
			commands[i].Handler = handlePlanCommand
		case "todos":
			commands[i].Handler = handleTodosCommand
		}
	}
}

func loadSkillSlashCommands() []slashCommand {
	var commands []slashCommand
	root := filepath.Join(config.RootDir(), "backend", "skills")
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		if command, ok := parseSkillCommand(path); ok {
			commands = append(commands, command)
		}
		return nil
	})
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

func handleExitCommand(_ *Model, _ string) tea.Cmd {
	return tea.Quit
}

func handleClearCommand(m *Model, _ string) tea.Cmd {
	m.resetConversationView()
	m.rt.ClearHistory()
	return nil
}

type dreamDoneMsg struct {
	output string
	err    error
}

func handleDreamCommand(m *Model, text string) tea.Cmd {
	m.pushMessage("user", text)
	m.pushMessage("system", "dream: running memory consolidation")
	return func() tea.Msg {
		result, err := m.rt.RunDream(context.Background())
		if err != nil {
			return dreamDoneMsg{err: err}
		}
		if !result.Success {
			message := strings.TrimSpace(result.Message)
			if message == "" {
				message = "dream failed"
			}
			return dreamDoneMsg{err: errors.New(message)}
		}
		return dreamDoneMsg{output: result.Output}
	}
}

func handleHelpCommand(m *Model, text string) tea.Cmd {
	m.pushMessage("user", text)
	arg := strings.TrimSpace(strings.TrimPrefix(text, "/help"))
	m.pushMessage("assistant", m.builtinHelp(arg))
	return nil
}

func handleHistoryCommand(m *Model, _ string) tea.Cmd {
	return m.handleHistoryCmd()
}

func handlePlanCommand(m *Model, text string) tea.Cmd {
	return m.handlePlanCmd(text)
}

func handleTodosCommand(m *Model, text string) tea.Cmd {
	return m.handleTodosCmd(text)
}

// shouldShowPopup gates popup visibility on the input value alone. The
// rule: input must start with "/" AND have no whitespace yet (still in
// the command-name region). Once the user types a space we're in the
// argument region — the menu disappears so /plan, /todos etc. can take
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
		score := func(command slashCommand) int {
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
		return score(out[i]) > score(out[j])
	})
	return out
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
