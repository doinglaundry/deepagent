package toolerrors

import "errors"

type resultError struct {
	err    error
	result string
}

func (e *resultError) Error() string { return e.err.Error() }
func (e *resultError) Unwrap() error { return e.err }

func AsResult(err error, result string) error {
	if err == nil {
		return nil
	}
	return &resultError{err: err, result: result}
}
func ShouldReturnAsResult(err error) bool { var e *resultError; return errors.As(err, &e) }
func Result(err error) string {
	var e *resultError
	if errors.As(err, &e) {
		return e.result
	}
	if err == nil {
		return ""
	}
	return err.Error()
}
