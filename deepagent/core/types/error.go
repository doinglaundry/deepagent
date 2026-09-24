package types

// InternalError distinguishes execution infrastructure failures from ordinary
// tool errors that can be returned to the model as an unsuccessful tool result.
type InternalError struct{ Err error }

func (e *InternalError) Error() string { return e.Err.Error() }
func (e *InternalError) Unwrap() error { return e.Err }
