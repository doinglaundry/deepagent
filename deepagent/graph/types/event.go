package types

import (
	"context"
	"time"

	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/schema"
)

type RuntimeEvent struct {
	Sequence uint64
	Kind     string
	CallID   string
	Data     any
}
type ToolChunkSink func(context.Context, ToolCall, string) error
type ModelChunkSink func(context.Context, *messagepkg.Message) error

// Event payloads are shared by graph producers and the thread event stream.
type LLMTokenChunk struct {
	Message       *messagepkg.Message `json:"-"`
	Text          string
	ReasoningText string
	LLMResponseID string
}
type LLMEnd struct {
	Message       *messagepkg.Message
	LLMResponseID string
}
type LLMRequestingPayload struct{ Messages []*messagepkg.Message }
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
	IsError         bool
}
