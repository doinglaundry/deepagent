package eino

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

func SuccessResult(output string) Result {
	return Result{Success: true, Output: output}
}
