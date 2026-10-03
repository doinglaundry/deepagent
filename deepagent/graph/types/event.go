package types

import (
	"context"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type RuntimeEvent struct {
	Sequence uint64
	Kind     string
	CallID   string
	Data     any
}
type ToolChunkSink func(context.Context, ToolCall, string) error
type ModelChunkSink func(context.Context, *schema.Message) error

// Event payloads are shared by graph producers and the thread event stream.
type LLMTokenChunk struct {
	Message       *schema.Message `json:"-"`
	Text          string
	ReasoningText string
	LLMResponseID string
}
type LLMEnd struct {
	model.CallbackOutput
	LLMResponseID string
}
type LLMRequestingPayload = model.CallbackInput
type ToolStartPayload struct {
	Name          string
	CallID        string
	Args          string
	ToolStartTime time.Time
}
type ToolCallOutputChunkPayload struct {
	Name   string
	CallID string
	Chunk  string
}
type ToolEndPayload struct {
	MultiContent    []schema.MessageInputPart `json:"-"`
	Name            string
	CallID          string
	ToolStartTime   time.Time
	ArgumentsInJSON string
	Result          string
}
