package model

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// ThreadOutputObservation is a read-only snapshot of one worker output item
// emitted by the Thread runtime.
type ThreadOutputObservation struct {
	SessionID string
	ThreadID  string
	Item      TransportThreadOutputItem
}

// ThreadOutputObserver is called after the Thread runtime has
// successfully offered one output item to the worker host. Implementations
// should return quickly and must not rely on mutating the observed item.
type ThreadOutputObserver func(ctx context.Context, obs ThreadOutputObservation)

type ThreadStatus = string

type ThreadProfile struct {
	Cwd string
}

const (
	ThreadStatusOpen    = "open"
	ThreadStatusClosing = "closing"
	ThreadStatusClosed  = "closed"
)

type ThreadRecord struct {
	ThreadID   int64      `gorm:"column:thread_id;primaryKey"`
	UserID     int64      `gorm:"column:user_id"`
	SessionID  string     `gorm:"column:session_id"`
	Status     string     `gorm:"column:status;size:32;index:idx_thread_lease,priority:1"`
	LeaseToken string     `gorm:"column:lease_token;size:191" json:"-" yaml:"-"`
	LeaseUntil *time.Time `gorm:"column:lease_until;index:idx_thread_lease,priority:2"`
	LastRunID  string     `gorm:"column:last_run_id;size:191"`

	// Read-only projections, never a second persisted state source.
	LastRun       *RunRecord        `gorm:"-"`
	PendingInputs int64             `gorm:"-"`
	Metadata      map[string]string `gorm:"column:metadata_json;type:text;serializer:coordinator_json"`
	Profile       *ThreadProfile    `gorm:"column:profile;type:text;serializer:coordinator_json"`
}

type ThreadFilter struct {
	Total         *int64
	IDs           []int64
	SessionIDs    []string
	Statuses      []string
	LeaseTokens   []string   `json:"lease_tokens" yaml:"lease_tokens"`
	LeaseValidAt  *time.Time `json:"lease_valid_at" yaml:"lease_valid_at"`
	RunnableUntil *time.Time `json:"runnable_until" yaml:"runnable_until"`
	Offset        int
	Limit         int
	Primary       bool
	ForUpdate     bool
}

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

func (t *ThreadRecord) OwnsLease(token string, now time.Time) bool {
	return token != "" && t.Status != ThreadStatusClosed && t.LeaseToken == token && t.LeaseUntil != nil && t.LeaseUntil.After(now)
}

// DisplayStatus is derived; only open/closing/closed are written to Thread.
func (t *ThreadRecord) DisplayStatus(now time.Time) string {
	if t.Status != ThreadStatusOpen {
		return t.Status
	}
	if t.LastRun != nil {
		if t.LastRun.Status == RunStatusBlocked {
			return "blocked"
		}
		// Worker 可在两轮之间保留租约；已结束且没有新输入时仍应显示空闲。
		finished := t.LastRun.Status == RunStatusFinished || t.LastRun.Status == RunStatusFailed || t.LastRun.Status == RunStatusInterrupted
		if finished && t.PendingInputs == 0 {
			return "idle"
		}
	}
	if t.OwnsLease(t.LeaseToken, now) {
		return "running"
	}
	if t.PendingInputs > 0 {
		return "ready"
	}
	return "idle"
}

func (ThreadRecord) TableName() (name string) { return "thread" }

func (f *ThreadFilter) DBFilter(query *gorm.DB) (filtered *gorm.DB) {
	if f.IDs != nil {
		query = query.Where("thread_id IN ?", f.IDs)
	}
	if f.SessionIDs != nil {
		query = query.Where("session_id IN ?", f.SessionIDs)
	}
	if f.Statuses != nil {
		query = query.Where("status IN ?", f.Statuses)
	}
	if f.LeaseTokens != nil {
		query = query.Where("lease_token IN ?", f.LeaseTokens)
	}
	if f.LeaseValidAt != nil {
		query = query.Where("lease_token <> '' AND lease_until > ?", *f.LeaseValidAt)
	}
	if f.RunnableUntil != nil {
		query = query.Where("thread.status IN ?", []string{ThreadStatusOpen, ThreadStatusClosing})
		query = query.Where("thread.lease_until IS NULL OR thread.lease_until <= ?", *f.RunnableUntil)
		// Pending commands can resume/cancel a blocked Run. Ordinary input cannot.
		// Accepted inputs from a nonterminal Run remain recoverable after a crash.
		query = query.Where(`thread.status = 'closing' OR EXISTS (
   SELECT 1 FROM message m LEFT JOIN agent_run r ON r.run_id = thread.last_run_id
   WHERE m.thread_id = thread.thread_id AND (
    (m.status = 'pending' AND (COALESCE(r.status, '') <> 'blocked' OR m.message_type = 'resume_run' OR m.message_type LIKE 'control.%'))
    OR (m.status = 'accepted' AND m.message_type NOT LIKE 'control.%' AND (
     (m.message_type = 'resume_run' AND r.status = 'blocked' AND JSON_UNQUOTE(JSON_EXTRACT(CONVERT(m.payload USING utf8mb4), '$.interrupt_id')) = r.interrupt_id)
     OR EXISTS (SELECT 1 FROM agent_run input_run WHERE input_run.run_id = m.trigger_turn_id AND input_run.status = 'started')
     OR m.trigger_turn_id = ''
    ))
   )
  )`)
	}

	return query
}

func (f *ThreadFilter) Page(query *gorm.DB) (paged *gorm.DB) {
	order := "thread_id ASC"
	if f.RunnableUntil != nil {
		order = "lease_until ASC, thread_id ASC"
	}
	if f.Offset > 0 {
		query = query.Offset(f.Offset)
	}
	if f.Limit > 0 {
		query = query.Limit(f.Limit)
	}
	return query.Order(order)
}

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
