package thread

import (
	"context"
	"errors"
	"time"
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

type TransportPendingBlock struct {
	RunID        string
	CheckpointID string
	InterruptID  string
}

type TransportThreadYield struct {
	Reason string
	Err    error
	Block  *TransportPendingBlock
}

type TransportThreadOutputItem struct {
	Event *TransportEvent
	Yield *TransportThreadYield
}

type TransportThreadOutput struct {
	Items <-chan TransportThreadOutputItem
}

type ThreadRuntime interface {
	Init(context.Context) (*TransportThreadOutput, error)
	PostMessage(context.Context, *TransportMessage) (*TransportPostMessageResult, error)
	Interrupt(context.Context, TransportThreadInterruptRequest) error
	ActiveRun() *TransportActiveRun
	Close(context.Context) error
}
