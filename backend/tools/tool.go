package tools

type InvocationStatus string

const (
	InvocationStatusRequested        InvocationStatus = "requested"
	InvocationStatusAwaitingApproval InvocationStatus = "awaiting_approval"
	InvocationStatusExecuting        InvocationStatus = "executing"
	InvocationStatusSucceeded        InvocationStatus = "succeeded"
	InvocationStatusFailed           InvocationStatus = "failed"
	InvocationStatusRejected         InvocationStatus = "rejected"
	InvocationStatusTimedOut         InvocationStatus = "timed_out"
)

type Tool struct {
	Name        string
	Description string
	Source      string
	Capability  string
	Execute     func(args []string, cwd string) (Result, error)
}

type Result struct {
	Output string
}
