package model

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

type SenderType = string

const (
	SenderTypeAgent               = "agent"
	SenderTypeSystem              = "system"
	RecordAgentManagerSenderID    = "manager"
	ControlTypeCancelInput        = "cancel_input"
	ControlTypeCloseThread        = "close_thread"
	ControlMessageTypePrefix      = "control."
	ControlMessageTypeCancelInput = "control.cancel_input"
	ControlMessageTypeCloseThread = "control.close_thread"
	MessageStatusPending          = "pending"
	MessageStatusAcked            = "acked"
	MessageStatusCompleted        = "completed"
	MessageStatusInterrupted      = "interrupted"
	MessageStatusCanceled         = "canceled"
)

func RecordNormalizeSenderType(sender SenderType) SenderType {
	if sender == "" {
		return SenderTypeSystem
	}
	return sender
}

type Sender struct {
	Type SenderType `gorm:"column:sender_type" json:"type"`
	ID   string     `gorm:"column:sender_id" json:"id"`
}

type Message struct {
	OutputKey    *string           `gorm:"column:output_key;size:255;uniqueIndex:idx_message_output,priority:2"`
	MessageID    int64             `gorm:"column:message_id;primaryKey"`
	ThreadID     int64             `gorm:"column:thread_id;uniqueIndex:idx_message_output,priority:1"`
	CreatedAt    time.Time         `gorm:"column:created_at"`
	Sender       *Sender           `gorm:"embedded"`
	MessageType  string            `gorm:"column:message_type"`
	Status       string            `gorm:"column:status"`
	Payload      []byte            `gorm:"column:payload;type:mediumblob"`
	Metadata     map[string]string `gorm:"column:metadata_json;type:text;serializer:coordinator_json"`
	TriggerRunID string            `gorm:"column:trigger_turn_id"`
}

func (Message) TableName() (name string) { return "message" }
func (m *Message) IsControl() bool {
	return m != nil && strings.HasPrefix(m.MessageType, ControlMessageTypePrefix)
}
func (m *Message) IsCloseControl() bool {
	return m != nil && m.MessageType == ControlMessageTypeCloseThread
}
func (m *Message) IsCancelControl() bool {
	return m != nil && m.MessageType == ControlMessageTypeCancelInput
}
func (m *Message) Normalize() error { return nil }

type MessageFilter struct {
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
	Primary         bool
}

func (f *MessageFilter) DBFilter(query *gorm.DB) *gorm.DB {
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

func (f *MessageFilter) Page(query *gorm.DB) *gorm.DB {
	order := "message_id ASC"
	if f.Desc {
		order = "message_id DESC"
	}
	if f.Offset > 0 {
		query = query.Offset(f.Offset)
	}
	if f.Limit > 0 {
		query = query.Limit(f.Limit)
	}
	return query.Order(order)
}
