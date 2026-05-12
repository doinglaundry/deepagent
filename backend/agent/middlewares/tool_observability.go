package middlewares

import (
	"context"
	"log/slog"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

// ToolCallObservability records one slog.Debug event per tool call
// (name / duration / argument size / result size or error). It is the ONE
// central point where the agent surfaces tool-call telemetry — individual
// tools stay log-free (see §5 of the rebuild_builtin_tools spec).
//
// Placement: must sit BEFORE NewToolErrorHandling in middleware_chain so
// it wraps the raw tool endpoint (inner layer) and observes the original
// error, before ToolErrorHandling rewrites errors into ToolMessage strings.
// See eino adk/wrappers_test.go:342 — slice order [h1,h2] executes as
// h2-before, h1-before, h1-after, h2-after (later slot = outer wrap).
//
// When disabled the WrapInvokableToolCall short-circuits to the unmodified
// endpoint, guaranteeing zero per-call overhead and zero log noise.
type ToolCallObservability struct {
	*adk.BaseChatModelAgentMiddleware
	enabled bool
}

// NewToolCallObservability returns the middleware. enabled is captured at
// construction time; flip the cfg.ToolObservability.Enabled flag and
// rebuild the agent to switch behavior.
func NewToolCallObservability(enabled bool) *ToolCallObservability {
	return &ToolCallObservability{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		enabled:                      enabled,
	}
}

// WrapInvokableToolCall is the only ChatModelAgentMiddleware hook this
// middleware overrides — every other lifecycle method falls through to the
// embedded base's no-op default.
func (o *ToolCallObservability) WrapInvokableToolCall(
	_ context.Context,
	endpoint adk.InvokableToolCallEndpoint,
	tCtx *adk.ToolContext,
) (adk.InvokableToolCallEndpoint, error) {
	if !o.enabled {
		return endpoint, nil
	}
	name := ""
	if tCtx != nil {
		name = tCtx.Name
	}
	return func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
		start := time.Now()
		out, err := endpoint(ctx, args, opts...)
		dur := time.Since(start)
		if err != nil {
			// Args / result contents are NEVER logged — only sizes — to
			// avoid leaking paths, command strings or file contents into
			// stderr logs (§5.6 of the spec).
			slog.Debug("tool.error",
				"name", name,
				"dur", dur,
				"in_size", len(args),
				"err", err,
			)
		} else {
			slog.Debug("tool.exit",
				"name", name,
				"dur", dur,
				"in_size", len(args),
				"out_size", len(out),
			)
		}
		return out, err
	}, nil
}
