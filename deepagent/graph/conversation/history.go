package conversation

import (
	"context"

	messagepkg "eino-cli/deepagent/message"
)

type CountTokenFunc func([]*messagepkg.Message) int

// 调用方只处理 Message；压缩存储格式和记录重放留在 DAL。
type ConversationDB interface {
	AppendMessage(context.Context, *messagepkg.Message) error
	SaveContext(context.Context, []*messagepkg.Message) error
	LoadContext(context.Context, string) (messages []*messagepkg.Message, recordedMessageIDs []string, lastReadSeq int64, err error)
}

// GetMessageIDFunc 为尚未分配身份的消息获取唯一 ID。
type GetMessageIDFunc func(ctx context.Context, message *messagepkg.Message) (string, error)
