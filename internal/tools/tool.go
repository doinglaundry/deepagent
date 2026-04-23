package tools

type RiskLevel string

type InvocationStatus string

const (
	RiskLevelLow    RiskLevel = "low"
	RiskLevelMedium RiskLevel = "medium"
	RiskLevelHigh   RiskLevel = "high"

	InvocationStatusRequested        InvocationStatus = "requested"
	InvocationStatusAwaitingApproval InvocationStatus = "awaiting_approval"
	InvocationStatusExecuting        InvocationStatus = "executing"
	InvocationStatusSucceeded        InvocationStatus = "succeeded"
	InvocationStatusFailed           InvocationStatus = "failed"
	InvocationStatusRejected         InvocationStatus = "rejected"
	InvocationStatusTimedOut         InvocationStatus = "timed_out"
)

type Tool struct {
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	RiskLevel        RiskLevel `json:"risk_level"`
	RequiresApproval bool      `json:"requires_approval"`
}

type Result struct {
	Output string `json:"output"`
}
