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

// Clarification mirrors deerflow.agents.middlewares.clarification_middleware.
//
// Real control flow (no eino interrupt API needed):
//   - When the model emits a tool call to `ask_clarification`, we parse the
//     JSON args, extract the question text, and REWRITE the assistant
//     message in-place: ToolCalls is cleared and Content is set to the
//     question. The deep-agent loop sees "no more tool calls → done" and
//     returns the rewritten message as the final output, exposing the
//     question to the REPL as a normal assistant turn. The user types an
//     answer, which becomes the next user message — the LLM treats it as
//     the answer to its own question. No checkpoint dance, no resume API.
//
// The mechanism relies on the deep-agent loop terminating when ToolCalls
// is empty, which is the standard ChatModelAgent contract.
type Clarification struct {
	*adk.BaseChatModelAgentMiddleware

	// OnQuestion (optional) is called whenever a clarification request is
	// detected. The host can use this for telemetry / custom rendering.
	// The middleware proceeds with the rewrite regardless.
	OnQuestion func(ctx context.Context, question string)

	Logger *slog.Logger

	mu sync.Mutex
}

// NewClarification returns a Clarification middleware with a default logger.
// Must always be the last ChatModelAgentMiddleware in the chain — same
// invariant as Python's "ClarificationMiddleware should always be last".
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

	// Locate the first ask_clarification call (Python takes only the first).
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

	// Rewrite the assistant message so the deep-agent loop terminates.
	// ToolCalls=nil triggers the "no further tools" exit branch; Content
	// becomes what the REPL surfaces to the user.
	display := question
	if strings.TrimSpace(display) == "" {
		display = "(model requested clarification but did not provide a question)"
	}
	last.ToolCalls = nil
	last.Content = display

	return ctx, state, nil
}

// parseClarificationArgs extracts the "question" field from the tool call's
// JSON arguments. Falls back to the raw arguments string when parsing fails
// so the user always sees something useful.
func parseClarificationArgs(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var args struct {
		Question string `json:"question"`
		Prompt   string `json:"prompt"`   // accept alternate keys
		Message  string `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		// Surface the raw args as best-effort.
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
