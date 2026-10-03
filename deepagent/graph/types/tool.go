package types

import (
	"github.com/cloudwego/eino/schema"
	"time"
)

type ToolCall struct {
	ID        string
	Index     int
	Name      string
	Arguments string
}
type ToolResult struct {
	CallID       string
	Content      string
	MultiContent []schema.MessageInputPart `json:",omitempty"`
	IsError      bool
	ReturnDirect bool
}
type CallStatus string

const (
	CallPending        CallStatus = "pending"
	CallRunning        CallStatus = "running"
	CallCompleted      CallStatus = "completed"
	CallBlocked        CallStatus = "blocked"
	CallOutcomeUnknown CallStatus = "outcome_unknown"
)

type ToolCallState struct {
	Call      ToolCall
	Status    CallStatus
	Result    *ToolResult
	StartedAt time.Time
}

// InternalError distinguishes execution infrastructure failures from ordinary
// tool errors that can be returned to the model as an unsuccessful tool result.
type InternalError struct{ Err error }

func (e *InternalError) Error() string { return e.Err.Error() }
func (e *InternalError) Unwrap() error { return e.Err }
