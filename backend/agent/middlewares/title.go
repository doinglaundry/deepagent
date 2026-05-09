package middlewares

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// Title mirrors deerflow.agents.middlewares.title_middleware. Once per
// thread, when the very first user message is seen, it triggers an async
// title generation. The generated title is exposed via OnTitleResolved so
// the host can persist it into session metadata.
//
// Phase 2 ships the detection + dedup machinery only; the actual model call
// and storage hookup land alongside the session-store refactor in Phase 3
// (the existing eino-cli session.Store does not yet have a Title field).
type Title struct {
	*adk.BaseChatModelAgentMiddleware

	// OnFirstUserMessage is invoked at most once per thread the first time a
	// user message is observed. It receives the raw user message content;
	// implementations should kick off async title generation.
	OnFirstUserMessage func(ctx context.Context, content string)

	Logger *slog.Logger

	mu      sync.Mutex
	visited bool
}

// NewTitle returns a Title middleware that simply logs the first-user-message
// event. Wire OnFirstUserMessage to plug in the actual title generation.
func NewTitle() *Title {
	return &Title{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		Logger:                       slog.Default(),
	}
}

func (m *Title) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelCtx *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil {
		return ctx, state, nil
	}

	m.mu.Lock()
	if m.visited {
		m.mu.Unlock()
		return ctx, state, nil
	}
	// Mark as visited optimistically so concurrent calls don't double-fire.
	m.visited = true
	m.mu.Unlock()

	for _, msg := range state.Messages {
		if msg == nil || msg.Role != schema.User {
			continue
		}
		content := strings.TrimSpace(msg.Content)
		if content == "" {
			continue
		}
		m.Logger.Debug("title: first user message detected",
			"chars", len(content))
		if m.OnFirstUserMessage != nil {
			go m.OnFirstUserMessage(ctx, content)
		}
		break
	}
	return ctx, state, nil
}
