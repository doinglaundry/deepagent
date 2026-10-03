package thread

import (
	context "context"
	errors "errors"
	time "time"
)

var TransportErrThreadClosed = errors.New("thread closed")

type TransportSenderType string

type TransportSender struct {
	Type TransportSenderType
	ID   string
}

type TransportMessageType string

type TransportMessage struct {
	ID       string
	Sender   *TransportSender
	Type     TransportMessageType
	Payload  []byte
	Metadata map[string]string
}

type TransportPostMessageResult struct{ RunID string }

type TransportThreadInterruptKind string

const (
	TransportThreadInterruptKindCancelInput           TransportThreadInterruptKind = "cancel_input"
	TransportThreadInterruptKindCloseThread           TransportThreadInterruptKind = "close_thread"
	TransportThreadInterruptKindWorkerShutdownTimeout TransportThreadInterruptKind = "worker_shutdown_timeout"
)

type TransportThreadInterruptRequest struct {
	Kind             TransportThreadInterruptKind
	Reason           string
	ControlMessageID string
	CutoffMessageID  string
	Timeout          *time.Duration
	Metadata         map[string]string
}

type TransportActiveRun struct {
	RunID              string
	ConsumedMessageIDs []string
}

type TransportEventType string

type TransportEvent struct {
	ID       string
	ThreadID string
	RunID    string
	Type     TransportEventType
	Payload  []byte
	Metadata map[string]string
	TS       time.Time
}

type TransportThreadYield struct {
	Reason string
	Err    error
}

type TransportThreadOutputItem struct {
	Err   error // Conversion or transport failure; this output must not be treated as a successful yield.
	Event *TransportEvent
	Yield *TransportThreadYield
}

type TransportThreadOutput struct {
	Items <-chan TransportThreadOutputItem
}

// context
// ThreadIdentity is the stable identity of one Run thread. It only holds
// plain values resolved from the Run thread spec; it never carries the
// raw AC thread struct, worker runtime objects, profile, cwd, metadata, UI
// fields or business extension fields.
type ContextThreadIdentity struct {
	ThreadID  string
	SessionID string
	UserID    int64
}

// RunIdentity is the stable identity of one Run run runner execution.
// MessageID identifies the worker message that started or resumed this run so
// observability integrations can correlate a runtime execution with its input.
type ContextRunIdentity struct {
	ThreadID  string
	RunID     string `json:"TurnID" yaml:"turnid"`
	MessageID string
}

type contextThreadInfoKey struct{}

type contextRunInfoKey struct{}

// ContextWithThreadIdentity returns a child context carrying the thread info value.
func ContextContextWithThreadIdentity(ctx context.Context, info ContextThreadIdentity) context.Context {
	return context.WithValue(ctx, contextThreadInfoKey{}, info)
}

// ThreadIdentityFromContext reports the thread info attached to ctx. The bool is false when
// no thread info was attached, so an empty value is not mistaken for a real one.
func ContextThreadIdentityFromContext(ctx context.Context) (ContextThreadIdentity, bool) {
	info, ok := ctx.Value(contextThreadInfoKey{}).(ContextThreadIdentity)
	return info, ok
}

// ContextWithRunIdentity returns a child context carrying the run info value.
func ContextContextWithRunIdentity(ctx context.Context, info ContextRunIdentity) context.Context {
	return context.WithValue(ctx, contextRunInfoKey{}, info)
}

// RunIdentityFromContext reports the run info attached to ctx. The bool is false when no
// run info was attached, so an empty value is not mistaken for a real one.
func ContextRunIdentityFromContext(ctx context.Context) (ContextRunIdentity, bool) {
	info, ok := ctx.Value(contextRunInfoKey{}).(ContextRunIdentity)
	return info, ok
}
