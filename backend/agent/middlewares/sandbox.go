package middlewares

import (
	"context"
	"log/slog"

	"github.com/cloudwego/eino/adk"

	"eino-cli/backend/sandbox"
)

// SandboxMiddleware acquires a per-thread sandbox before the agent runs
// and stamps the sandbox id onto ctx so downstream tools (and any nested
// agents that inherit the ctx) hit the same sandbox.
//
// Lazy semantics intentionally NOT implemented: every M2 tool that touches
// the filesystem checks GetSandboxID, so by-tool laziness is enough — we
// don't need a separate lazy_init flag like deer-flow's Python version.
type SandboxMiddleware struct {
	*adk.BaseChatModelAgentMiddleware

	Manager sandbox.SandboxManager
	Logger  *slog.Logger
}

// NewSandbox wires the manager. Passing nil makes the middleware a no-op,
// which is how CLI mode without a sandbox configured stays compatible.
func NewSandbox(manager sandbox.SandboxManager) *SandboxMiddleware {
	return &SandboxMiddleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		Manager:                      manager,
		Logger:                       slog.Default(),
	}
}

// BeforeAgent runs once per agent invocation. We pull the thread id from
// ctx (the gateway / CLI is expected to stamp it), Acquire the sandbox,
// and re-stamp ctx with the sandbox id so tools can find it.
func (m *SandboxMiddleware) BeforeAgent(
	ctx context.Context,
	runCtx *adk.ChatModelAgentContext,
) (context.Context, *adk.ChatModelAgentContext, error) {
	if m.Manager == nil {
		return ctx, runCtx, nil
	}
	// Idempotent: if a parent agent already acquired, reuse.
	if GetSandboxID(ctx) != "" {
		return ctx, runCtx, nil
	}
	tid := GetThreadID(ctx)
	sid, err := m.Manager.Acquire(ctx, tid)
	if err != nil {
		// Failing acquire shouldn't crash the agent — fall through with no
		// sandbox stamped, tools will degrade to host-fs and HITL gating.
		m.Logger.Warn("sandbox middleware: acquire failed, continuing without sandbox",
			"thread_id", tid, "error", err)
		return ctx, runCtx, nil
	}
	m.Logger.Debug("sandbox middleware: acquired", "thread_id", tid, "sandbox_id", sid)
	return WithSandboxID(ctx, sid), runCtx, nil
}
