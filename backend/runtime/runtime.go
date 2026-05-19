package runtime

import "context"

type StreamChunkHandler func(chunk string)

type ErrorCode string

const (
	ErrorCodeRuntime ErrorCode = "runtime_error"
)

type Result struct {
	Success   bool      `json:"success"`
	Code      ErrorCode `json:"code,omitempty"`
	Message   string    `json:"message,omitempty"`
	Output    string    `json:"output,omitempty"`
	NeedsUser bool      `json:"needs_user,omitempty"`
}

type Runtime interface {
	ExecuteStream(ctx context.Context, prompt string, onChunk StreamChunkHandler) (Result, error)
	ClearHistory()
	ExportHistory() ([]byte, error)
	ImportHistory(payload []byte) error
	RollbackToHistory(payload []byte) error
	// SetPlanMode toggles the per-turn TodoInstruction injection. Cheap
	// — atomic flag flip, no agent rebuild — so callers can flip it on
	// every key press if they want. Returns the new state for the
	// caller's convenience (TUI footer / system message).
	SetPlanMode(ctx context.Context, on bool) (bool, error)
	Name() string
}
