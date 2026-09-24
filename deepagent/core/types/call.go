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
type ResumeAnswer struct {
	InterruptID string
	CallID      string
	Approved    bool
	Data        any
}
