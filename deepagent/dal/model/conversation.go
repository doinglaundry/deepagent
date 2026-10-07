package model

import "github.com/cloudwego/eino/schema"

type ConversationEntryType string

const (
	ConversationEntryMessage ConversationEntryType = "message"
	ConversationEntryCompact ConversationEntryType = "compact"
)

// ConversationEntry 保存一条普通消息，或一次压缩后的完整对话上下文。
type ConversationEntry struct {
	ThreadID          string                `gorm:"column:thread_id;primaryKey;size:128"`
	MessageID         int64                 `gorm:"column:message_id;primaryKey;autoIncrement:false"`
	Seq               int64                 `gorm:"column:seq"`
	RunID             string                `gorm:"column:turn_id;size:128"`
	Type              ConversationEntryType `gorm:"column:type;size:32"`
	Message           *schema.Message       `gorm:"column:message;type:longtext;serializer:coordinator_json"`
	CompactedMessages []*schema.Message     `gorm:"column:ext;type:longtext;serializer:coordinator_json"`
	CreatedAt         int64                 `gorm:"column:created_at;autoCreateTime:false"`
}

func (ConversationEntry) TableName() string { return "agentthread_history" }
