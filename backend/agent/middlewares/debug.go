package middlewares

import (
	"context"
	"sync/atomic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// Debug phase tags. Untyped int constants on purpose: the field on
// DebugEvent is plain int so any int can be passed; we still get
// readable named values at all sensible call sites.
const (
	DebugBefore = iota + 1
	DebugAfter
)

// DebugEvent is one half-turn snapshot.
//
//   Phase=DebugBefore: Messages = the entire slice the model is
//     about to consume (after every preceding middleware mutated it).
//   Phase=DebugAfter:  Messages = a 1-element slice holding the new
//     assistant message (content + tool_calls).
//
// Turn is monotonic per Trace instance, 1-indexed; the Before hook
// increments it and the After hook reuses the same value so consumers
// can pair them up.
//
// AgentName tags the originating agent (lead or subagent) so that when
// subagents recurse, their events stay distinguishable as they
// interleave with the lead's on the same DebugConsumer.
type DebugEvent struct {
	AgentName string
	Phase     int
	Turn      int
	Messages  []*schema.Message
}

// DebugConsumer is the receiving end of a Trace event stream. The
// middleware calls Send for each phase. NOTE: this Send is unrelated
// to bubbletea's Program.Send — the TUI provides its own adapter that
// happens to forward to it.
type DebugConsumer interface {
	Send(DebugEvent)
}

type debugConsumerKey struct{}

// WithDebugConsumer derives a child context that carries the consumer.
// Passing a nil consumer is a no-op (returns the parent unchanged), so
// the caller doesn't need a separate "if debug { ... }" branch.
func WithDebugConsumer(ctx context.Context, consumer DebugConsumer) context.Context {
	if consumer == nil {
		return ctx
	}
	return context.WithValue(ctx, debugConsumerKey{}, consumer)
}

func getDebugConsumerFromContext(ctx context.Context) DebugConsumer {
	c, _ := ctx.Value(debugConsumerKey{}).(DebugConsumer)
	return c
}

// Trace is a no-cost middleware unless a DebugConsumer is attached to
// the per-call ctx. When attached, it sends a DebugBefore event at the
// start of each model turn (with the full message slice, post-mutation
// by every preceding middleware) and a DebugAfter event at the end
// (with just the new assistant message). Each emitted event is tagged
// with the owning agent's name so subagent runs interleaved on the same
// consumer remain visually distinguishable.
//
// MUST be registered immediately BEFORE Clarification (i.e. as the
// last read-only middleware). Both Before and After hooks dispatch in
// registration order; Clarification's After hook rewrites the assistant
// message in-place (clears ToolCalls, replaces Content with the
// question), so Trace has to run first to capture the model's raw
// response.
//
// The lead agent's Trace is exposed by MakeLeadAgent (via FindTrace) so
// the runtime can call ResetTurn() from /clear; subagent Traces are
// short-lived (one per ExecuteStream) and don't need explicit reset.
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

// ResetTurn rewinds the turn counter so the next BeforeModelRewriteState
// observes Turn=1. Wired to Runtime.ClearHistory: after /clear wipes
// the conversation history, restarting turn numbering at 1 matches the
// user's mental model ("clear means clear").
func (t *Trace) ResetTurn() { t.turn.Store(0) }

func (t *Trace) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	consumer := getDebugConsumerFromContext(ctx)
	if consumer == nil || state == nil {
		return ctx, state, nil
	}
	consumer.Send(DebugEvent{
		AgentName: t.agentName,
		Phase:     DebugBefore,
		Turn:      int(t.turn.Add(1)),
		// Copy the slice header — subsequent middlewares may still
		// mutate state.Messages even after the model returns. We
		// don't deep-copy each message; *schema.Message is treated
		// as immutable post-send.
		Messages: append([]*schema.Message(nil), state.Messages...),
	})
	return ctx, state, nil
}

func (t *Trace) AfterModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	_ *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	consumer := getDebugConsumerFromContext(ctx)
	if consumer == nil || state == nil || len(state.Messages) == 0 {
		return ctx, state, nil
	}
	consumer.Send(DebugEvent{
		AgentName: t.agentName,
		Phase:     DebugAfter,
		Turn:      int(t.turn.Load()),
		Messages:  []*schema.Message{state.Messages[len(state.Messages)-1]},
	})
	return ctx, state, nil
}

// FindTrace returns the *Trace embedded in a middleware list, or nil if
// absent. Lets MakeLeadAgent pull the lead's Trace out of the chain
// after GetChatModelMiddlewares built it, keeping the chain builder
// strictly responsible for "build the chain" and nothing else.
func FindTrace(list []adk.ChatModelAgentMiddleware) *Trace {
	for _, mw := range list {
		if t, ok := mw.(*Trace); ok {
			return t
		}
	}
	return nil
}
