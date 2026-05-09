package middlewares

import (
	"context"
	"log/slog"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// TokenUsage mirrors deerflow.agents.middlewares.token_usage_middleware. It
// accumulates per-call prompt/completion token counts read from the model's
// response metadata. The deerflow Python version pushes the totals into a
// session-level counter; here we expose Snapshot() so the host can decide
// where to store them.
type TokenUsage struct {
	*adk.BaseChatModelAgentMiddleware

	Logger *slog.Logger

	mu    sync.Mutex
	stats TokenUsageStats
}

// TokenUsageStats is the read-only counter surface.
type TokenUsageStats struct {
	PromptTokens     int64
	CompletionTokens int64
	TotalTokens      int64
	Calls            int
}

// NewTokenUsage returns a TokenUsage middleware. Attach it via BuildChain
// when AppConfig.TokenUsage.Enabled is true.
func NewTokenUsage() *TokenUsage {
	return &TokenUsage{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		Logger:                       slog.Default(),
	}
}

// Snapshot returns a copy of the current accumulated stats.
func (m *TokenUsage) Snapshot() TokenUsageStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stats
}

func (m *TokenUsage) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelCtx *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}
	last := state.Messages[len(state.Messages)-1]
	if last == nil || last.Role != schema.Assistant {
		return ctx, state, nil
	}

	usage := extractUsage(last)

	m.mu.Lock()
	m.stats.PromptTokens += usage.prompt
	m.stats.CompletionTokens += usage.completion
	m.stats.TotalTokens += usage.total
	m.stats.Calls++
	m.mu.Unlock()

	if usage.total > 0 {
		m.Logger.Debug("token usage",
			"prompt", usage.prompt,
			"completion", usage.completion,
			"total", usage.total,
			"call", m.stats.Calls)
	}
	return ctx, state, nil
}

// usageRecord captures the trio of counters extracted from a message.
type usageRecord struct {
	prompt, completion, total int64
}

// extractUsage probes the message for ResponseMeta-style usage data. eino's
// schema.Message exposes a *ResponseMeta with Usage, but message providers
// can also stash usage into Extra. We try both spots.
func extractUsage(msg *schema.Message) usageRecord {
	var rec usageRecord
	if msg == nil {
		return rec
	}
	if msg.ResponseMeta != nil && msg.ResponseMeta.Usage != nil {
		u := msg.ResponseMeta.Usage
		rec.prompt = int64(u.PromptTokens)
		rec.completion = int64(u.CompletionTokens)
		rec.total = int64(u.TotalTokens)
		if rec.total == 0 {
			rec.total = rec.prompt + rec.completion
		}
		return rec
	}
	if msg.Extra != nil {
		if v, ok := msg.Extra["prompt_tokens"]; ok {
			rec.prompt = toInt64(v)
		}
		if v, ok := msg.Extra["completion_tokens"]; ok {
			rec.completion = toInt64(v)
		}
		if v, ok := msg.Extra["total_tokens"]; ok {
			rec.total = toInt64(v)
		}
		if rec.total == 0 {
			rec.total = rec.prompt + rec.completion
		}
	}
	return rec
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	}
	return 0
}
