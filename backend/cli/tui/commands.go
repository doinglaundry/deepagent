package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"eino-cli/backend/config"
)

type slashCommandHandler func(*Model, string) tea.Cmd

type slashCommand struct {
	Name    string
	Args    string
	Desc    string
	Type    string
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
	resetConversationView(m)
	m.rt.ClearHistory()
	return nil
}

type dreamDoneMsg struct {
	output string
	err    error
}

func handleDreamCommand(m *Model, text string) tea.Cmd {
	pushMessage(m, "user", text)
	pushMessage(m, "system", "dream: running memory consolidation")
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
	pushMessage(m, "user", text)
	arg := strings.TrimSpace(strings.TrimPrefix(text, "/help"))
	pushMessage(m, "assistant", buildBuiltinHelp(getAvailableCommands(m), arg))
	return nil
}

func handleHistoryCommand(m *Model, _ string) tea.Cmd {
	rows, err := m.runs.ListRuns(context.Background())
	if err != nil {
		pushMessage(m, "system", fmt.Sprintf("history: %v", err))
		return nil
	}
	if len(rows) == 0 {
		pushMessage(m, "system", "history: no runs yet")
		return nil
	}
	m.runHistoryRows = rows
	m.runHistorySel = 0
	m.runHistoryOpen = true
	m.input.Reset()
	recomputeLayout(m)
	return nil
}

func handlePlanCommand(m *Model, text string) tea.Cmd {
	arg := strings.TrimSpace(strings.TrimPrefix(text, "/plan"))
	want := m.planMode
	switch strings.ToLower(arg) {
	case "", "toggle":
		want = !m.planMode
	case "on":
		want = true
	case "off":
		want = false
	default:
		pushMessage(m, "system", "usage: /plan [on|off|toggle]")
		return nil
	}
	got, err := m.rt.SetPlanMode(context.Background(), want)
	if err != nil {
		pushMessage(m, "system", fmt.Sprintf("plan: %v", err))
		return nil
	}
	m.planMode = got
	state := "off"
	if got {
		state = "on"
	}
	pushMessage(m, "system", fmt.Sprintf("plan mode = %s", state))
	return nil
}

func handleTodosCommand(m *Model, text string) tea.Cmd {
	arg := strings.TrimSpace(strings.TrimPrefix(text, "/todos"))
	prevH := getTodoPanelHeight(m)
	switch strings.ToLower(arg) {
	case "", "toggle":
		m.todoExpanded = !m.todoExpanded
	case "open":
		m.todoExpanded = true
	case "close":
		m.todoExpanded = false
	default:
		pushMessage(m, "system", "usage: /todos [open|close|toggle]")
		return nil
	}
	if getTodoPanelHeight(m) != prevH {
		recomputeLayout(m)
	}
	state := "closed"
	if m.todoExpanded {
		state = "open"
	}
	pushMessage(m, "system", fmt.Sprintf("todos panel = %s", state))
	return nil
}

func applyBuiltin(m *Model, text string) (tea.Cmd, bool) {
	if !strings.HasPrefix(text, "/") {
		return nil, false
	}
	name := strings.TrimSpace(strings.TrimPrefix(strings.SplitN(text, " ", 2)[0], "/"))
	command, ok := getCommand(getAvailableCommands(m), name)
	if !ok || command.Handler == nil {
		return nil, false
	}
	return command.Handler(m, text), true
}

// shouldShowPopup: input starts with "/" and has no whitespace yet.
func shouldShowPopup(input string) bool {
	if !strings.HasPrefix(input, "/") {
		return false
	}
	return !strings.ContainsAny(input, " \t")
}

func getPopupMatches(m *Model) []slashCommand {
	return filterCommands(getAvailableCommands(m), m.input.Value())
}

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

func getCommand(commands []slashCommand, name string) (slashCommand, bool) {
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
	if _, found := getCommand(commands, name); !found {
		return ""
	}
	return name
}

func buildBuiltinHelp(commands []slashCommand, target string) string {
	if target != "" {
		if command, ok := getCommand(commands, target); ok {
			kind := "Built-in command"
			if command.Type == "skill" {
				kind = "Skill"
			}
			args := ""
			if command.Args != "" {
				args = " " + command.Args
			}
			return fmt.Sprintf("**/%s%s** — _%s_\n\n%s", command.Name, args, kind, command.Desc)
		}
		return fmt.Sprintf("Unknown command: `/%s`. Run `/help` to see available commands.", strings.TrimPrefix(target, "/"))
	}

	var builtins, skills []string
	for _, command := range commands {
		args := ""
		if command.Args != "" {
			args = " " + command.Args
		}
		line := fmt.Sprintf("- `/%s%s` — %s", command.Name, args, command.Desc)
		if command.Type == "skill" {
			skills = append(skills, line)
		} else {
			builtins = append(builtins, line)
		}
	}
	var sb strings.Builder
	sb.WriteString("**Available slash commands**\n\n_Built-in_\n")
	sb.WriteString(strings.Join(builtins, "\n"))
	if len(skills) > 0 {
		sb.WriteString("\n\n_Skills_\n")
		sb.WriteString(strings.Join(skills, "\n"))
	}
	sb.WriteString("\n\nRun `/help <name>` for details. Press Ctrl-C during a response to abort, Ctrl-O to expand the latest tool block, or Ctrl-C twice from idle to quit.")
	return sb.String()
}

func builtinHelp() string {
	return buildBuiltinHelp(commands, "")
}
