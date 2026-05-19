package middlewares

import (
	"context"
	"sync/atomic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/schema"
)

const (
	TracePhaseBefore = iota + 1
	TracePhaseAfter
	TracePhaseTodos
	TracePhaseTokens
)

// TraceEvent is one half-turn snapshot. Before carries the full message
// slice; After carries just the new assistant message; Todos carries the
// session-key-todos snapshot when present; Tokens carries the running
// token-usage snapshot when TokenUsage middleware is attached. Turn
// pairs Before with After. Same struct, different fields filled per
// phase — same pattern as Messages already meaning "full slice" vs
// "single delta" by phase.
type TraceEvent struct {
	AgentName string
	Phase     int
	Turn      int
	Messages  []*schema.Message
	Todos     []deep.TODO      // only set when Phase == TracePhaseTodos
	Tokens    *TokenUsageStats // only set when Phase == TracePhaseTokens
}

// TraceConsumer is the receiving end of a Trace event stream.
type TraceConsumer interface {
	Send(TraceEvent)
}

type traceConsumerKey struct{}

// WithTraceConsumer attaches a TraceConsumer to ctx; nil consumer is a no-op.
func WithTraceConsumer(ctx context.Context, consumer TraceConsumer) context.Context {
	if consumer == nil {
		return ctx
	}
	return context.WithValue(ctx, traceConsumerKey{}, consumer)
}

// GetTraceConsumer returns the TraceConsumer attached to ctx, or nil when
// WithTraceConsumer was never called. Exported so the runtime/run layer
// layer can extract the consumer it just installed (and tests can drive
// trace events directly).
func GetTraceConsumer(ctx context.Context) TraceConsumer {
	c, _ := ctx.Value(traceConsumerKey{}).(TraceConsumer)
	return c
}

// Trace emits Before/After events to a TraceConsumer in ctx (no-op when none).
// MUST sit immediately before Clarification so it captures the raw assistant
// message before Clarification rewrites it in-place.
type Trace struct {
	*adk.BaseChatModelAgentMiddleware
	agentName string
	turn      atomic.Int64

	// TokenSnapshot, when non-nil, fires a TracePhaseTokens event after
	// every model turn carrying the current cumulative token counters.
	// Wired by GetChatModelMiddlewares iff cfg.TokenUsage.Enabled, so
	// the field stays nil in test stubs / disabled runs and the After
	// hook short-circuits the emission. Field over a separate struct
	// because TokenUsage is the only metric we'd plumb this way today
	// (AGENTS.md "矫枉过正预警" — 8+ fields before a struct).
	TokenSnapshot func() TokenUsageStats
}

func NewTrace(agentName string) *Trace {
	return &Trace{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		agentName:                    agentName,
	}
}

// ResetTurn rewinds the turn counter so the next Before observes Turn=1
// (called by Runtime.ClearHistory).
func (t *Trace) ResetTurn() { t.turn.Store(0) }

func (t *Trace) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	consumer := GetTraceConsumer(ctx)
	if consumer == nil || state == nil {
		return ctx, state, nil
	}
	consumer.Send(TraceEvent{
		AgentName: t.agentName,
		Phase:     TracePhaseBefore,
		Turn:      int(t.turn.Add(1)),
		Messages:  append([]*schema.Message(nil), state.Messages...),
	})
	return ctx, state, nil
}

func (t *Trace) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	consumer := GetTraceConsumer(ctx)
	if consumer == nil || state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}
	consumer.Send(TraceEvent{
		AgentName: t.agentName,
		Phase:     TracePhaseAfter,
		Turn:      int(t.turn.Load()),
		Messages:  []*schema.Message{state.Messages[len(state.Messages)-1]},
	})

	// Piggy-back the current todo snapshot onto the same After hook —
	// emitting it here (rather than in the write_todos tool) keeps the
	// TUI in sync after Summarisation / interrupt-resume rebuilds where
	// the tool wouldn't fire again. Empty / missing → skip silently.
	if raw, ok := adk.GetSessionValue(ctx, deep.SessionKeyTodos); ok {
		todos, _ := raw.([]deep.TODO)
		if len(todos) > 0 {
			consumer.Send(TraceEvent{
				AgentName: t.agentName,
				Phase:     TracePhaseTodos,
				Turn:      int(t.turn.Load()),
				Todos:     todos,
			})
		}
	}

	// Token snapshot rides the same After hook — TokenUsage already
	// counted this turn before Trace runs (chain order). Pointer (not
	// value) keeps TraceEvent's by-value channel send cheap when the
	// other phases don't fill Tokens.
	if t.TokenSnapshot != nil {
		stats := t.TokenSnapshot()
		consumer.Send(TraceEvent{
			AgentName: t.agentName,
			Phase:     TracePhaseTokens,
			Turn:      int(t.turn.Load()),
			Tokens:    &stats,
		})
	}
	return ctx, state, nil
}

// FindTrace returns the *Trace embedded in a middleware list, or nil if absent.
func FindTrace(list []adk.ChatModelAgentMiddleware) *Trace {
	for _, mw := range list {
		if t, ok := mw.(*Trace); ok {
			return t
		}
	}
	return nil
}
