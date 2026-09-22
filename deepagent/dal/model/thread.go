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
	ThreadStatusIdle    = "idle"
	ThreadStatusReady   = "ready"
	ThreadStatusRunning = "running"
	ThreadStatusBlocked = "blocked"
	ThreadStatusClosing = "closing"
	ThreadStatusClosed  = "closed"
)

type Thread struct {
	ThreadID   int64     `gorm:"column:thread_id;primaryKey"`
	ReadyUntil time.Time `gorm:"column:ready_until"`
	UserID     int64     `gorm:"column:user_id"`
	SessionID  string    `gorm:"column:session_id"`
	Status     string    `gorm:"column:status"`
	LeaseToken string    `gorm:"column:lease_token" json:"-" yaml:"lease_token"`

	Metadata map[string]string `gorm:"column:metadata_json;type:text;serializer:coordinator_json"`
	Profile  *Profile          `gorm:"column:profile;type:text;serializer:coordinator_json"`
}

func (Thread) TableName() (name string) { return "thread" }

type ThreadFilter struct {
	Total           *int64
	IDs             []int64
	SessionIDs      []string
	Statuses        []string
	LeaseTokens     []string   `json:"lease_tokens" yaml:"lease_tokens"`
	LeaseValidAt    *time.Time `json:"lease_valid_at" yaml:"lease_valid_at"`
	ReadyUntilAfter *time.Time `json:"ready_until_after" yaml:"ready_until_after"`
	RunnableUntil   *time.Time `json:"runnable_until" yaml:"runnable_until"`
	Offset          int
	Limit           int
	Primary         bool
	ForUpdate       bool
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
		query = query.Where("ready_until >= ?", *f.LeaseValidAt)
	}
	if f.ReadyUntilAfter != nil {
		query = query.Where("ready_until > ?", *f.ReadyUntilAfter)
	}
	if f.RunnableUntil != nil {
		query = query.Where("status IN ?", []string{ThreadStatusReady, ThreadStatusRunning, ThreadStatusClosing})
		query = query.Where("ready_until <= ?", *f.RunnableUntil)
	}
	return query
}

func (f *ThreadFilter) Page(query *gorm.DB) (paged *gorm.DB) {
	order := "thread_id ASC"
	if f.RunnableUntil != nil {
		order = "ready_until ASC, thread_id ASC"
	}
	if f.Offset > 0 {
		query = query.Offset(f.Offset)
	}
	if f.Limit > 0 {
		query = query.Limit(f.Limit)
	}
	return query.Order(order)
}
