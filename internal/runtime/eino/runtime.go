package eino

import "context"

type StreamChunkHandler func(chunk string)

type Runtime interface {
	Execute(ctx context.Context, prompt string) (Result, error)
	ExecuteStream(ctx context.Context, prompt string, onChunk StreamChunkHandler) (Result, error)
	Name() string
}
