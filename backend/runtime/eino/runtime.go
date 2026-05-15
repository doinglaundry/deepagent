package eino

import "context"

type StreamChunkHandler func(chunk string)

type Runtime interface {
	ExecuteStream(ctx context.Context, prompt string, onChunk StreamChunkHandler) (Result, error)
	ClearHistory()
	// SetPlanMode toggles the per-turn TodoInstruction injection. Cheap
	// — atomic flag flip, no agent rebuild — so callers can flip it on
	// every key press if they want. Returns the new state for the
	// caller's convenience (TUI footer / system message).
	SetPlanMode(ctx context.Context, on bool) (bool, error)
	Name() string
}
