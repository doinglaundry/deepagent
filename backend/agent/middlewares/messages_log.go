package middlewares

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type MessagesLog struct {
	*adk.BaseChatModelAgentMiddleware
	path string
	seen int
}

func NewMessagesLog(path string) *MessagesLog {
	return &MessagesLog{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		path:                         path,
	}
}

func (m *MessagesLog) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	if m.path == "" || state == nil {
		return ctx, state, nil
	}
	start := m.seen
	if start > len(state.Messages) {
		start = 0
	}
	messages := state.Messages[start:]
	m.seen = len(state.Messages)
	if len(messages) == 0 {
		return ctx, state, nil
	}
	appendMessagesLog(m.path, messages)
	return ctx, state, nil
}

func appendMessagesLog(path string, messages []*schema.Message) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		slog.Warn("messages log: mkdir failed", "path", path, "err", err)
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		slog.Warn("messages log: open failed", "path", path, "err", err)
		return
	}
	defer f.Close()

	encoder := json.NewEncoder(f)
	for _, msg := range messages {
		record := map[string]any{
			"created_at": time.Now().UTC(),
			"type":       msg.Role,
			"message":    msg,
		}
		if err := encoder.Encode(record); err != nil {
			slog.Warn("messages log: write failed", "path", path, "err", err)
			return
		}
	}
}
