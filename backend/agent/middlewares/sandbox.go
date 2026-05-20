package middlewares

import (
	"context"
	"log/slog"

	"github.com/cloudwego/eino/adk"

	runtimecontext "eino-cli/backend/runtime/context"
	"eino-cli/backend/sandbox"
)

// SandboxMiddleware acquires a per-thread sandbox in BeforeAgent and stamps the sid onto ctx.
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
	tid := runtimecontext.GetThreadID(ctx)
	sid, err := m.Manager.Acquire(ctx, tid)
	if err != nil {
		// Acquire failure must not crash the run; tools degrade to host fs.
		m.Logger.Warn("sandbox middleware: acquire failed, continuing without sandbox",
			"thread_id", tid, "error", err)
		return ctx, runCtx, nil
	}
	m.Logger.Debug("sandbox middleware: acquired", "thread_id", tid, "sandbox_id", sid)
	return runtimecontext.WithSandboxID(ctx, sid), runCtx, nil
}
