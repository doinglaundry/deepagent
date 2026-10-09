package model

import (
	"context"
	"time"
)

type InputMessage struct {
	SenderType  MailboxSenderType
	SenderID    string
	MessageType string
	Payload     []byte
	Metadata    map[string]string
}

type SubmitRequest struct {
	ThreadID  int64
	UserID    int64
	SessionID string
	Title     string
	Metadata  map[string]string
	Profile   *ThreadProfile
	Input     *InputMessage
}

type ListMessagesRequest struct {
	ThreadID  int64
	SessionID string
	RunID     string
	AfterID   int64
	Limit     int32
	Offset    int
	Backward  bool
}

type ListMessagesResult struct {
	Messages []*MailboxMessage
	Runs     map[string]*RunRecord
	Total    int64
}

type CancelInputControlPayload struct {
	ControlType     string `json:"control_type"`
	RequestID       string `json:"request_id"`
	ThreadID        int64  `json:"thread_id"`
	CutoffMessageID int64  `json:"cutoff_message_id"`
	Reason          string `json:"reason,omitempty"`
}

// ManagerClient defines the scheduling operations consumed by Worker.
type ManagerClient interface {
	Acquire(context.Context, AcquireRequest) (AcquireResult, error)
	Renew(context.Context, int64, string, int64) (*Lease, error)
	ReleaseThread(context.Context, int64, string) (*ThreadRecord, error)
	AckInput(context.Context, int64, string, string, []int64) ([]*MailboxMessage, error)
	ConfirmThreadClosed(context.Context, int64, string, int64) (*ThreadMessageResult, error)
	SaveOutput(context.Context, int64, string, string, []OutputFrame) error
}

type OutputFrame struct {
	EventID   int64             `json:"event_id"`
	QueueID   string            `json:"queue_id,omitempty"`
	ThreadID  int64             `json:"thread_id"`
	SessionID string            `json:"session_id"`
	RunID     string            `json:"run_id"`
	EventType string            `json:"event_type"`
	Payload   []byte            `json:"payload"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
}

type SubscribeSessionRequest struct {
	SessionID      string
	RecoverQueueID string
}

type Subscription struct {
	Events <-chan OutputFrame
	Err    error
	Close  func() error
}

type AcquireRequest struct {
	ThreadID   int64
	LeaseToken string
	LeaseMS    int64
}

type Lease struct {
	ThreadID   int64     `json:"thread_id"`
	LeaseToken string    `json:"lease_token"`
	LeaseUntil time.Time `json:"lease_until"`
}

type AcquireResult struct {
	Thread          *ThreadRecord
	Lease           *Lease
	PendingMessages []*MailboxMessage
	ServerTimeMS    int64
}

type ThreadMessageResult struct {
	Thread  *ThreadRecord
	Message *MailboxMessage
}

type ListThreadsRequest struct {
	ThreadID  int64
	SessionID string
	Limit     int32
	Offset    int
}

type ListThreadsResult struct {
	Thread  *ThreadRecord
	Threads []*ThreadRecord
	Total   int64
}

type CloseThreadControlPayload struct {
	ControlType string `json:"control_type"`
	RequestID   string `json:"request_id"`
	ThreadID    int64  `json:"thread_id"`
	Reason      string `json:"reason,omitempty"`
}

type CollaborationBackend interface {
	Submit(context.Context, SubmitRequest) (ThreadMessageResult, error)
	ListThreads(context.Context, ListThreadsRequest) (ListThreadsResult, error)
	ListMessages(context.Context, ListMessagesRequest) (ListMessagesResult, error)
	Close(context.Context, int64, string) (*ThreadMessageResult, error)
}

// WorkerCancelInputControlPayload reads the fields needed to cancel execution.
type WorkerCancelInputControlPayload struct {
	CutoffMessageID int64  `json:"cutoff_message_id"`
	Reason          string `json:"reason,omitempty"`
}

// WorkerCloseThreadControlPayload reads the reason for closing a Thread.
type WorkerCloseThreadControlPayload struct {
	Reason string `json:"reason,omitempty"`
}
