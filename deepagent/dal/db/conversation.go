package db

import (
	"context"
	"errors"
	"strings"

	"eino-cli/deepagent/dal/cache"
	"eino-cli/deepagent/dal/model"
	"gorm.io/gorm/clause"
)

// ConversationDAO uses the same MySQL connection and transaction context as other DAOs.
type ConversationDAO struct {
	Client    *MySQLClient
	Redis     cache.RedisClient
	tableName string
}

func NewConversationDAO(client *MySQLClient, tableName string, redis cache.RedisClient) *ConversationDAO {
	if tableName == "" {
		tableName = model.ConversationEntry{}.TableName()
	}
	return &ConversationDAO{Client: client, tableName: tableName, Redis: redis}
}

func (conversationDAO *ConversationDAO) Append(ctx context.Context, conversationEntry *model.ConversationEntry) error {
	if conversationDAO == nil || conversationDAO.Client == nil || conversationDAO.Redis == nil {
		return errors.New("conversation persistence requires MySQL and Redis")
	}
	if conversationEntry == nil || strings.TrimSpace(conversationEntry.ThreadID) == "" || conversationEntry.MessageID <= 0 {
		return errors.New("history record requires a thread id and positive message id")
	}
	if conversationEntry.Seq == 0 {
		sequence, err := cache.GenerateSequence(ctx, conversationDAO.Redis, "deepagent:history:seq:thread:"+conversationEntry.ThreadID)
		if err != nil {
			return err
		}
		conversationEntry.Seq = sequence
	}
	if conversationEntry.Seq <= 0 {
		return errors.New("history sequence must be positive")
	}
	database := conversationDAO.Client.DB(ctx, true).Table(conversationDAO.tableName)
	result := database.Clauses(clause.OnConflict{DoNothing: true}).Create(conversationEntry)
	if result.Error != nil {
		return result.Error
	}
	// Always use the durable sequence; duplicate row counts depend on the MySQL client configuration.
	return database.Select("seq").Where("thread_id = ? AND message_id = ?", conversationEntry.ThreadID, conversationEntry.MessageID).Scan(&conversationEntry.Seq).Error
}
func (conversationDAO *ConversationDAO) LoadAfter(ctx context.Context, threadID string, sequence int64, limit int) ([]*model.ConversationEntry, error) {
	if conversationDAO == nil || conversationDAO.Client == nil {
		return nil, errors.New("history store is not initialized")
	}
	var conversationEntries []*model.ConversationEntry
	err := conversationDAO.Client.DB(ctx, true).Table(conversationDAO.tableName).
		Where("thread_id = ? AND seq > ?", threadID, sequence).
		Order("seq ASC").Limit(limit).Find(&conversationEntries).Error
	return conversationEntries, err
}
