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

// parseClarificationArgs extracts question/prompt/message from the JSON args;
// falls back to the raw string when JSON parsing fails.
func parseClarificationArgs(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var args struct {
		Question string `json:"question"`
		Prompt   string `json:"prompt"`
		Message  string `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return fmt.Sprintf("(unparsed clarification args: %s)", raw)
	}
	switch {
	case strings.TrimSpace(args.Question) != "":
		return strings.TrimSpace(args.Question)
	case strings.TrimSpace(args.Prompt) != "":
		return strings.TrimSpace(args.Prompt)
	case strings.TrimSpace(args.Message) != "":
		return strings.TrimSpace(args.Message)
	}
	return raw
}
