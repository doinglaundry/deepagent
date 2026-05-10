package middlewares

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// Title fires OnFirstUserMessage at most once per thread when the first
// user message arrives — used by hosts to kick off async title generation.
type Title struct {
	*adk.BaseChatModelAgentMiddleware

	OnFirstUserMessage func(ctx context.Context, content string)

	Logger *slog.Logger

	mu      sync.Mutex
	visited bool
}

// NewTitle returns a Title middleware; wire OnFirstUserMessage to enable.
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
