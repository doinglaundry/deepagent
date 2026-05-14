package middlewares

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// AskClarificationToolName is the conventional tool name the lead-agent
// prompt instructs the model to call when it needs user input.
const AskClarificationToolName = "ask_clarification"

// Clarification rewrites an `ask_clarification` tool call into a plain
// assistant question, ending the deep-agent loop so the REPL can prompt
// the user. MUST be the last ChatModelAgentMiddleware in the chain.
type Clarification struct {
	*adk.BaseChatModelAgentMiddleware

	OnQuestion func(ctx context.Context, question string)

	Logger *slog.Logger

	mu sync.Mutex
}

// NewClarification returns a Clarification middleware with a default logger.
func NewClarification() *Clarification {
	return &Clarification{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		Logger:                       slog.Default(),
	}
}

func (m *Clarification) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelCtx *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}
	last := state.Messages[len(state.Messages)-1]
	if last == nil || last.Role != schema.Assistant || len(last.ToolCalls) == 0 {
		return ctx, state, nil
	}

	var question string
	found := false
	for _, call := range last.ToolCalls {
		if call.Function.Name == AskClarificationToolName {
			question = parseClarificationArgs(call.Function.Arguments)
			found = true
			break
		}
	}
	if !found {
		return ctx, state, nil
	}

	m.mu.Lock()
	logger := m.Logger
	m.mu.Unlock()
	logger.Info("clarification requested", "question", question)

	if m.OnQuestion != nil {
		m.OnQuestion(ctx, question)
	}

	// ToolCalls=nil ends the deep-agent loop; Content becomes the user-facing reply.
	display := question
	if strings.TrimSpace(display) == "" {
		display = "(model requested clarification but did not provide a question)"
	}
	last.ToolCalls = nil
	last.Content = display

	return ctx, state, nil
}

// parseClarificationArgs formats structured clarification args for display;
// falls back to prompt/message/raw so imperfect model calls still surface.
func parseClarificationArgs(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var args struct {
		Question          string          `json:"question"`
		Prompt            string          `json:"prompt"`
		Message           string          `json:"message"`
		ClarificationType string          `json:"clarification_type"`
		Context           string          `json:"context"`
		Options           json.RawMessage `json:"options"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return fmt.Sprintf("(unparsed clarification args: %s)", raw)
	}
	question := getClarificationQuestion(args.Question, args.Prompt, args.Message)
	if question == "" {
		return raw
	}

	var parts []string
	if context := strings.TrimSpace(args.Context); context != "" {
		parts = append(parts, context, "")
	}
	parts = append(parts, question)
	if options := parseClarificationOptions(args.Options); len(options) > 0 {
		parts = append(parts, "")
		for i, option := range options {
			parts = append(parts, fmt.Sprintf("%d. %s", i+1, option))
		}
	}
	return strings.Join(parts, "\n")
}

func getClarificationQuestion(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func parseClarificationOptions(raw json.RawMessage) []string {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil
	}
	var options []string
	if err := json.Unmarshal(raw, &options); err == nil {
		return cleanClarificationOptions(options)
	}

	var encoded string
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil
	}
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(encoded), &options); err == nil {
		return cleanClarificationOptions(options)
	}
	return cleanClarificationOptions([]string{encoded})
}

func cleanClarificationOptions(options []string) []string {
	out := make([]string, 0, len(options))
	for _, option := range options {
		if trimmed := strings.TrimSpace(option); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
