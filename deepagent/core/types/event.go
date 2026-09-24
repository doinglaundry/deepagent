package types

import (
	"context"
	"github.com/cloudwego/eino/schema"
)

type RuntimeEvent struct {
	Sequence uint64
	Kind     string
	CallID   string
	Data     any
}
type EventSink interface {
	Emit(context.Context, RuntimeEvent) error
}
type EventSinkFunc func(context.Context, RuntimeEvent) error

func (f EventSinkFunc) Emit(ctx context.Context, event RuntimeEvent) error { return f(ctx, event) }

type eventSinkKey struct{}

func WithEventSink(ctx context.Context, sink EventSink) context.Context {
	return context.WithValue(ctx, eventSinkKey{}, sink)
}

// EventDeliveryError is an infrastructure failure, never an ordinary tool error.
type EventDeliveryError struct{ Err error }

func (e *EventDeliveryError) Error() string { return "runtime event delivery: " + e.Err.Error() }
func (e *EventDeliveryError) Unwrap() error { return e.Err }
func EmitEvent(ctx context.Context, event RuntimeEvent) error {
	sink, _ := ctx.Value(eventSinkKey{}).(EventSink)
	if sink == nil {
		return nil
	}
	if err := sink.Emit(ctx, event); err != nil {
		return &EventDeliveryError{Err: err}
	}
	return nil
}

type ToolChunkSink func(context.Context, ToolCall, string) error
type ToolOutputChunk struct {
	Call    ToolCall
	Content string
}
type ModelChunkSink func(context.Context, *schema.Message) error
