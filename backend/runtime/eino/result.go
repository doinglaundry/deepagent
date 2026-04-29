package eino

type ErrorCode string

const (
	ErrorCodeConfig    ErrorCode = "config_error"
	ErrorCodeWorkspace ErrorCode = "workspace_error"
	ErrorCodeRuntime   ErrorCode = "runtime_error"
	ErrorCodeTool      ErrorCode = "tool_error"
)

type Result struct {
	Success   bool      `json:"success"`
	Code      ErrorCode `json:"code,omitempty"`
	Message   string    `json:"message,omitempty"`
	Output    string    `json:"output,omitempty"`
	NeedsUser bool      `json:"needs_user,omitempty"`
}

func SuccessResult(output string) Result {
	return Result{Success: true, Output: output}
}

func FailureResult(code ErrorCode, message string) Result {
	return Result{Success: false, Code: code, Message: message}
}
