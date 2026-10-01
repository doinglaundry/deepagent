package model

import (
	"time"

	"gorm.io/gorm"
)

type ThreadStatus = string

type Profile struct {
	Role string
	Cwd  string
}

const (
	ThreadStatusOpen    = "open"
	ThreadStatusClosing = "closing"
	ThreadStatusClosed  = "closed"
)

type Thread struct {
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
	Profile       *Profile          `gorm:"column:profile;type:text;serializer:coordinator_json"`
}

func (t *Thread) OwnsLease(token string, now time.Time) bool {
	return token != "" && t.Status != ThreadStatusClosed && t.LeaseToken == token && t.LeaseUntil != nil && t.LeaseUntil.After(now)
}

// DisplayStatus is derived; only open/closing/closed are written to Thread.
func (t *Thread) DisplayStatus(now time.Time) string {
	if t.Status != ThreadStatusOpen {
		return t.Status
	}
	if t.LastRun != nil && t.LastRun.Status == "blocked" {
		return "blocked"
	}
	if t.OwnsLease(t.LeaseToken, now) {
		return "running"
	}
	if t.PendingInputs > 0 {
		return "ready"
	}
	return "idle"
}

func (Thread) TableName() (name string) { return "thread" }

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
