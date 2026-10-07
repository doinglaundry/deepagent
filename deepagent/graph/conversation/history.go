package conversation

import (
	"context"

	dalmodel "eino-cli/deepagent/dal/model"
	"github.com/cloudwego/eino/schema"
)

type TokenCounter func([]*schema.Message) int

// ConversationRepository 负责保存对话条目，并按顺序加载历史。
type ConversationRepository interface {
	Append(context.Context, *dalmodel.ConversationEntry) error
	LoadAfter(ctx context.Context, threadID string, sequence int64, limit int) ([]*dalmodel.ConversationEntry, error)
}

type ConversationEntryIDProvider func(context.Context, string, string, *schema.Message) (int64, error)
