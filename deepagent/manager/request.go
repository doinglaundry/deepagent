package manager

import (
	"errors"
	"time"

	"eino-cli/deepagent/dal/db"
	"eino-cli/deepagent/dal/model"
)

type SubmitRequest struct {
	ThreadID  int64
	UserID    int64
	SessionID string
	Title     string
	Metadata  map[string]string
	Profile   *model.Profile
	Input     *InputMessage
}

type InputMessage struct {
	SenderType  model.SenderType
	SenderID    string
	MessageType string
	Payload     []byte
	Metadata    map[string]string
}

type ThreadMessageResult struct {
	Thread  *model.Thread
	Message *db.Message
}

const (
	defaultLeaseDuration         = time.Minute
	maxLeaseDuration             = 30 * time.Minute
	defaultFailureReleaseBackoff = 3 * time.Second
	defaultScanLimit             = int32(50)
	maxScanLimit                 = int32(100)
	queuedMessageLimit           = 10
	DefaultCancelInputReason     = "user_cancel"
	DefaultCloseThreadReason     = "user_close"
)

var defaultFailureReleaseReasons = map[string]struct{}{}

var (
	ErrThreadNotFound          = errors.New("thread not found")
	ErrThreadClosed            = errors.New("thread is closing or closed")
	ErrThreadNotRunnable       = errors.New("thread not runnable")
	ErrThreadNotBlocked        = errors.New("thread is not blocked")
	ErrThreadBlocked           = errors.New("thread is blocked")
	ErrLeaseMismatch           = errors.New("lease mismatch")
	ErrRedisUnavailable        = errors.New("redis unavailable")
	ErrInvalidStatusTransition = errors.New("invalid status transition")
	ErrInvalidCancel           = errors.New("invalid cancel")
	ErrInvalidClose            = errors.New("invalid close")
	ErrMessageNotFound         = errors.New("message not found")
	InputErrMessageNotFound    = errors.New("input message not found")
	ErrOutputUnavailable       = errors.New("output unavailable")
	ErrRunIDRequired           = errors.New("run ID required")
)

type CancelInputControlPayload struct {
	ControlType     string `json:"control_type"`
	RequestID       string `json:"request_id"`
	ThreadID        int64  `json:"thread_id"`
	CutoffMessageID int64  `json:"cutoff_message_id"`
	Reason          string `json:"reason,omitempty"`
}

type CloseThreadControlPayload struct {
	ControlType string `json:"control_type"`
	RequestID   string `json:"request_id"`
	ThreadID    int64  `json:"thread_id"`
	Reason      string `json:"reason,omitempty"`
}

type AcquireRequest struct {
	ThreadID   int64
	LeaseToken string
	LeaseMS    int64
	ScanLimit  int32
}

type Lease struct {
	ThreadID   int64     `json:"thread_id"`
	LeaseToken string    `json:"lease_token"`
	LeaseUntil time.Time `json:"lease_until"`
}

type AcquireResult struct {
	Thread          *model.Thread
	Lease           *Lease
	PendingMessages []*db.Message
	ServerTimeMS    int64
}

type ListThreadsRequest struct {
	ThreadID  int64
	SessionID string
	Limit     int32
	Offset    int
}

type ListThreadsResult struct {
	Thread  *model.Thread
	Threads []*model.Thread
	Total   int64
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
	Messages []*db.Message
	Total    int64
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
