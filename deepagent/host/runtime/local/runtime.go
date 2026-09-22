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
	RunDream(ctx context.Context) (Result, error)
	ClearHistory()
	ExportHistory() ([]byte, error)
	ImportHistory(payload []byte) error
	RollbackToHistory(payload []byte) error
	SetPlanMode(ctx context.Context, on bool) (bool, error)
	Name() string
}
