package model

import (
	"context"
	"time"

	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ToolDescriptor carries the tool and its execution capabilities from the constructor.
// ToolSet registers this descriptor; execution uses the original Eino Tool.
type ToolDescriptor struct {
	Tool             einotool.BaseTool
	ReadOnly         bool
	RequiresApproval bool
	ParallelSafe     bool
	ReturnDirect     bool
}

type Mask func(context.Context, *schema.ToolInfo) bool

type ApprovalInfo struct {
	CallID    string
	ToolName  string
	Arguments string
	Reason    string
}

type ApprovalResult struct {
	CallID           string
	Approved         bool
	CancelRun        bool
	DisapproveReason *string
}

// Policy decides before a tool can produce a side effect.
type Policy interface {
	Decide(context.Context, ToolCall, ToolDescriptor) (Decision, error)
}

type PolicyFunc func(context.Context, ToolCall, ToolDescriptor) (Decision, error)

type Action string

const (
	Allow       Action = "allow"
	Deny        Action = "deny"
	AskApproval Action = "ask_approval"
)

type Decision struct {
	Action Action
	Reason string
}

type ChildRequest struct {
	Name          string
	Prompt        string
	MaxModelCalls int
}

type ChildRunner interface {
	Run(context.Context, ChildRequest, ModelChunkSink) (*Message, error)
}

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

func init() {
	schema.RegisterName[*ApprovalInfo]("deepagent_approval_info")
}

func (policyFunc PolicyFunc) Decide(ctx context.Context, toolCall ToolCall, toolDescriptor ToolDescriptor) (Decision, error) {
	return policyFunc(ctx, toolCall, toolDescriptor)
}

func (internalError *InternalError) Error() string { return internalError.Err.Error() }

func (internalError *InternalError) Unwrap() error { return internalError.Err }
