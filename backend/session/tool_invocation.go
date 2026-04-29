package session

import "time"

type ApprovalStatus string

type ExecutionStatus string

const (
	ApprovalStatusNotRequired      ApprovalStatus = "not_required"
	ApprovalStatusAwaitingApproval ApprovalStatus = "awaiting_approval"
	ApprovalStatusApproved         ApprovalStatus = "approved"
	ApprovalStatusRejected         ApprovalStatus = "rejected"

	ExecutionStatusRequested ExecutionStatus = "requested"
	ExecutionStatusExecuting ExecutionStatus = "executing"
	ExecutionStatusSucceeded ExecutionStatus = "succeeded"
	ExecutionStatusFailed    ExecutionStatus = "failed"
	ExecutionStatusRejected  ExecutionStatus = "rejected"
)

type ToolInvocation struct {
	ID              string          `json:"id"`
	ToolName        string          `json:"tool_name"`
	Arguments       []string        `json:"arguments"`
	ApprovalStatus  ApprovalStatus  `json:"approval_status"`
	ExecutionStatus ExecutionStatus `json:"execution_status"`
	Output          string          `json:"output,omitempty"`
	ErrorMessage    string          `json:"error_message,omitempty"`
	CreatedAt       time.Time       `json:"created_at"`
}
