package model

import (
	"context"

	einomodel "github.com/cloudwego/eino/components/model"
)

// Conversation 定义 Graph 使用的历史、压缩和用量能力。
type Conversation interface {
	ContextManager
	BuildRequest(context.Context, []*Message) ([]*Message, error)
	SnapshotContext() (int64, ContextTokenUsage)
	RestoreContext(context.Context, int64, ContextTokenUsage) error
	GetRunUsage() RunUsage
	RestoreRunUsage(context.Context, RunUsage) error
}

type CountTokenFunc func([]*Message) int

// 调用方只处理 Message；压缩存储格式和记录重放留在 DAL。
type ConversationDB interface {
	AppendMessage(context.Context, *Message) error
	SaveContext(context.Context, []*Message) error
	LoadContext(context.Context, string) (messages []*Message, recordedMessageIDs []string, lastReadSeq int64, err error)
}

// GetMessageIDFunc 为尚未分配身份的消息获取唯一 ID。
type GetMessageIDFunc func(ctx context.Context, message *Message) (string, error)

// ContextManager preserves the Thread context contract.
type ContextManager interface {
	ReloadHistory(context.Context) error
	AddHistory(context.Context, string, ...*Message) error
	GetHistory(context.Context) []*Message
	GetContextUsage() ContextTokenUsage
	RecordModelUsage(context.Context, *einomodel.TokenUsage)
	Compact(context.Context, string) (*ContextTokenUsage, error)
	NeedsCompaction(context.Context) bool
}
