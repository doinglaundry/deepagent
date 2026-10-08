package conversation

import (
	"context"

	messagepkg "eino-cli/deepagent/message"
)

type TokenCounter func([]*messagepkg.Message) int

// 调用方只处理 Message；压缩存储格式和记录重放留在 DAL。
type ConversationRepository interface {
	AppendMessage(context.Context, *messagepkg.Message) error
	SaveContext(context.Context, *messagepkg.Message, []*messagepkg.Message) error
	LoadContext(context.Context, string) (messages []*messagepkg.Message, messageIDs []string, sequence int64, err error)
}
type MessageIDProvider func(context.Context, *messagepkg.Message) (string, error)
