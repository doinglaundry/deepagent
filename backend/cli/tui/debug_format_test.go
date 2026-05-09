package tui

import (
	"strings"
	"testing"

	"eino-cli/backend/agent/middlewares"

	"github.com/cloudwego/eino/schema"
)

// formatDebugInput must put the agent name in a [name] prefix on the
// header line, ahead of the turn / message-count / size summary, so
// interleaved subagent events are visually distinguishable.
func TestFormatDebugInput_HasAgentPrefix(t *testing.T) {
	ev := middlewares.DebugEvent{
		AgentName: "DeerFlow",
		Phase:     middlewares.DebugBefore,
		Turn:      3,
		Messages: []*schema.Message{
			schema.SystemMessage("you are a helpful assistant"),
			schema.UserMessage("hi"),
		},
	}

	got := formatDebugInput(ev)
	if !strings.HasPrefix(got, "[DeerFlow] turn 3 input ·") {
		t.Errorf("formatDebugInput: header = %q, want prefix %q", firstLine(got), "[DeerFlow] turn 3 input ·")
	}
	if !strings.Contains(got, "[system]") || !strings.Contains(got, "[user]") {
		t.Errorf("formatDebugInput: expected per-message [role] lines, got:\n%s", got)
	}
}

func TestFormatDebugOutput_HasAgentPrefix(t *testing.T) {
	ev := middlewares.DebugEvent{
		AgentName: "researcher",
		Phase:     middlewares.DebugAfter,
		Turn:      1,
		Messages: []*schema.Message{
			schema.AssistantMessage("here you go", nil),
		},
	}

	got := formatDebugOutput(ev)
	if !strings.HasPrefix(got, "[researcher] turn 1 output") {
		t.Errorf("formatDebugOutput: header = %q, want prefix %q", firstLine(got), "[researcher] turn 1 output")
	}
}

// /help output must mention /debug — otherwise users can't discover
// the toggle.
func TestBuiltinHelpMentionsDebug(t *testing.T) {
	if !strings.Contains(builtinHelp(), "/debug") {
		t.Errorf("builtinHelp() missing /debug entry:\n%s", builtinHelp())
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
