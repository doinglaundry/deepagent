package middlewares

import (
	"context"
	"sync/atomic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

const (
	TracePhaseBefore = iota + 1
	TracePhaseAfter
)

// TraceEvent is one half-turn snapshot. Before carries the full message
// slice; After carries just the new assistant message. Turn pairs them up.
type TraceEvent struct {
	AgentName string
	Phase     int
	Turn      int
	Messages  []*schema.Message
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

func getTraceConsumerFromContext(ctx context.Context) TraceConsumer {
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
	consumer := getTraceConsumerFromContext(ctx)
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
	consumer := getTraceConsumerFromContext(ctx)
	if consumer == nil || state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}
	consumer.Send(TraceEvent{
		AgentName: t.agentName,
		Phase:     TracePhaseAfter,
		Turn:      int(t.turn.Load()),
		Messages:  []*schema.Message{state.Messages[len(state.Messages)-1]},
	})
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
