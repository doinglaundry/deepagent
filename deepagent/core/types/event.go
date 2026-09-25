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
type ToolChunkSink func(context.Context, ToolCall, string) error
type ToolOutputChunk struct {
	Call    ToolCall
	Content string
}
type ModelChunkSink func(context.Context, *schema.Message) error
