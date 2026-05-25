package middlewares

import (
	"context"
	"log/slog"

	"github.com/cloudwego/eino/adk"

	"eino-cli/backend/consts"
	runtimecontext "eino-cli/backend/runtime/context"
	"eino-cli/backend/sandbox"
)

// SandboxMiddleware acquires a per-session sandbox in BeforeAgent and stamps the sid onto ctx.
type SandboxMiddleware struct {
	*adk.BaseChatModelAgentMiddleware

	Manager sandbox.SandboxManager
	Logger  *slog.Logger
}

// NewSandbox wraps manager; passing nil makes the middleware a no-op.
func NewSandbox(manager sandbox.SandboxManager) *SandboxMiddleware {
	return &SandboxMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		Manager:                      manager,
		Logger:                       slog.Default(),
	}
}

// BeforeAgent acquires the sandbox once per agent invocation.
func (m *SandboxMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if m.Manager == nil {
		return ctx, runCtx, nil
	}
	if runtimecontext.GetSandboxID(ctx) != "" {
		return ctx, runCtx, nil
	}
	sessionID := runtimecontext.GetSessionID(ctx)
	if sessionID == "" {
		sessionID = m.Manager.SessionID()
		if sessionID == "" {
			sessionID = consts.DefaultSessionID
		}
		ctx = runtimecontext.WithSessionID(ctx, sessionID)
	}
	sid, err := m.Manager.GetSandboxIdBySessionId(ctx, sessionID)
	if err != nil {
		m.Logger.Warn("sandbox middleware: acquire failed, continuing without sandbox",
			"session_id", sessionID, "error", err)
		return ctx, runCtx, nil
	}
	m.Logger.Debug("sandbox middleware: acquired", "session_id", sessionID, "sandbox_id", sid)
	return runtimecontext.WithSandboxID(ctx, sid), runCtx, nil
}
