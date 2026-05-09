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

// renderMessage for an "assistant" entry must not start with a
// newline: the green ⏺ marker and the model's reply have to share the
// first line, otherwise the chat looks like the model emitted an
// empty turn followed by a body. Guards against glamour leaking its
// document margin (leading "\n" / spaces) into the rendered field.
func TestRenderMessage_AssistantPrefixSameLine(t *testing.T) {
	m := &Model{}
	m.viewport.Width = 80

	const reply = "Why don't scientists trust atoms? Because they make up everything!"
	rendered := m.renderMarkdown(reply)
	out := m.renderMessage(chatMessage{Role: "assistant", Content: reply, Rendered: rendered})

	if strings.HasPrefix(out, "\n") || strings.HasPrefix(rendered, "\n") {
		t.Fatalf("assistant render starts with newline; ⏺ marker and body should share line 1.\nrendered=%q\nout=%q", rendered, out)
	}
	if !strings.Contains(firstLine(out), "atoms") {
		t.Errorf("first line should hold the start of the reply; got %q", firstLine(out))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
