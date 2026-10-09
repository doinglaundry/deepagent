package model

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

type MailboxSenderType = string

const (
	MailboxSenderTypeAgent        = "agent"
	MailboxSenderTypeSystem       = "system"
	RecordAgentManagerSenderID    = "manager"
	ControlTypeCancelInput        = "cancel_input"
	ControlTypeCloseThread        = "close_thread"
	ControlMessageTypePrefix      = "control."
	ControlMessageTypeCancelInput = "control.cancel_input"
	ControlMessageTypeCloseThread = "control.close_thread"
	MessageStatusPending          = "pending"
	MessageStatusAccepted         = "accepted"
	MessageStatusCanceled         = "canceled"
)

type MailboxSender struct {
	Type MailboxSenderType `gorm:"column:sender_type" json:"type"`
	ID   string            `gorm:"column:sender_id" json:"id"`
}

type MailboxMessage struct {
	OutputKey    *string           `gorm:"column:output_key;size:255;uniqueIndex:idx_message_output,priority:2"`
	MessageID    int64             `gorm:"column:message_id;primaryKey"`
	ThreadID     int64             `gorm:"column:thread_id;uniqueIndex:idx_message_output,priority:1;index:idx_message_delivery,priority:1"`
	CreatedAt    time.Time         `gorm:"column:created_at"`
	Sender       *MailboxSender    `gorm:"embedded"`
	MessageType  string            `gorm:"column:message_type"`
	Status       string            `gorm:"column:status;size:32;index:idx_message_delivery,priority:2"`
	Payload      []byte            `gorm:"column:payload;type:mediumblob"`
	Metadata     map[string]string `gorm:"column:metadata_json;type:text;serializer:coordinator_json"`
	TriggerRunID string            `gorm:"column:trigger_turn_id;size:191;index"`
}

type MailboxMessageFilter struct {
	Total           *int64
	InputOnly       bool
	AfterID         *int64
	OutputKey       *string
	SessionID       string
	RunID           string
	ExcludeControls bool
	Take            bool
	SkipNormalize   bool
	ThreadIDs       []int64
	IDs             []int64
	Statuses        []string
	BeforeID        *int64
	Desc            bool
	Offset          int
	Limit           int
	PriorityFirst   bool
	PriorityOnly    bool
	Primary         bool
}

const (
	MessageTypeControl = "control"
	MessageTypeOutput  = "output"
)

func RecordNormalizeSenderType(sender MailboxSenderType) MailboxSenderType {
	if sender == "" {
		return MailboxSenderTypeSystem
	}
	return sender
}

func (MailboxMessage) TableName() (name string) { return "message" }

func (m *MailboxMessage) IsControl() bool {
	return m != nil && strings.HasPrefix(m.MessageType, ControlMessageTypePrefix)
}

func (m *MailboxMessage) IsCloseControl() bool {
	return m != nil && m.MessageType == ControlMessageTypeCloseThread
}

func (m *MailboxMessage) IsCancelControl() bool {
	return m != nil && m.MessageType == ControlMessageTypeCancelInput
}

func (m *MailboxMessage) Normalize() error { return nil }

func (f *MailboxMessageFilter) DBFilter(query *gorm.DB) *gorm.DB {
	if f.PriorityOnly {
		query = query.Where("message_type = ? OR message_type LIKE ?", "resume_run", ControlMessageTypePrefix+"%")
	}
	if f.InputOnly {
		query = query.Where("output_key IS NULL AND message_type NOT LIKE ?", ControlMessageTypePrefix+"%")
	}
	if f.ThreadIDs != nil {
		query = query.Where("thread_id IN ?", f.ThreadIDs)
	}
	if f.IDs != nil {
		query = query.Where("message_id IN ?", f.IDs)
	}
	if f.Statuses != nil {
		query = query.Where("status IN ?", f.Statuses)
	}
	if f.BeforeID != nil {
		query = query.Where("message_id < ?", *f.BeforeID)
	}
	if f.AfterID != nil {
		query = query.Where("message_id > ?", *f.AfterID)
	}
	if f.OutputKey != nil {
		query = query.Where("output_key = ?", *f.OutputKey)
	}
	if f.SessionID != "" {
		query = query.Where("thread_id IN (SELECT thread_id FROM thread WHERE session_id = ?)", f.SessionID)
	}
	if f.RunID != "" {
		query = query.Where("trigger_turn_id = ?", f.RunID)
	}
	if f.ExcludeControls {
		query = query.Where("message_type NOT LIKE ?", ControlMessageTypePrefix+"%")
	}
	return query
}

func (f *MailboxMessageFilter) Page(query *gorm.DB) *gorm.DB {
	order := "message_id ASC"
	if f.Desc {
		order = "message_id DESC"
	}
	if f.PriorityFirst {
		order = "CASE WHEN message_type = 'resume_run' OR message_type LIKE 'control.%' THEN 0 ELSE 1 END, CASE WHEN message_type = 'resume_run' OR message_type LIKE 'control.%' THEN message_id END DESC, message_id ASC"
	}
	if f.Offset > 0 {
		query = query.Offset(f.Offset)
	}
	if f.Limit > 0 {
		query = query.Limit(f.Limit)
	}
	return query.Order(order)
}
