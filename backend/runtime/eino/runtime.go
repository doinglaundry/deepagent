package eino

import "context"

type StreamChunkHandler func(chunk string)

type Runtime interface {
	Execute(ctx context.Context, prompt string) (Result, error)
	ExecuteStream(ctx context.Context, prompt string, onChunk StreamChunkHandler) (Result, error)
	ClearHistory()
	Name() string

	// SetPlanMode flips plan mode on the underlying agent. Implementations
	// should be no-op when the value is unchanged and rebuild whatever
	// internal state needs the new prompt / middleware list.
	SetPlanMode(ctx context.Context, plan bool) error
}
