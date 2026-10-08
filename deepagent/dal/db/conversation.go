package db

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"eino-cli/deepagent/dal/cache"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm/clause"
)

// conversationRow 只是数据库格式，不会传入 Graph、Thread 或 Conversation。
type conversationRow struct {
	ThreadID          string                `gorm:"column:thread_id;primaryKey;size:128"`
	MessageID         string                `gorm:"column:message_id;primaryKey;size:128"`
	Seq               int64                 `gorm:"column:seq"`
	RunID             string                `gorm:"column:turn_id;size:128"`
	Type              string                `gorm:"column:type;size:32"`
	Message           *messagepkg.Message   `gorm:"column:message;type:longtext;serializer:coordinator_json"`
	CompactedMessages []*messagepkg.Message `gorm:"column:ext;type:longtext;serializer:coordinator_json"`
	CreatedAt         int64                 `gorm:"column:created_at;autoCreateTime:false"`
}

// ConversationDAO uses the same MySQL connection and transaction context as other DAOs.
type ConversationDAO struct {
	Client    *MySQLClient
	Redis     cache.RedisClient
	tableName string
}

func NewConversationDAO(client *MySQLClient, tableName string, redis cache.RedisClient) *ConversationDAO {
	if tableName == "" {
		tableName = "agentthread_history"
	}
	return &ConversationDAO{Client: client, tableName: tableName, Redis: redis}
}

// AppendMessage 保存业务消息，使用 Manager 的 MySQL 连接和事务。
func (dao *ConversationDAO) AppendMessage(ctx context.Context, message *messagepkg.Message) error {
	return dao.saveMessage(ctx, message, nil)
}
func (dao *ConversationDAO) SaveContext(ctx context.Context, summary *messagepkg.Message, messages []*messagepkg.Message) error {
	if summary == nil || len(messages) == 0 || messages[0] != summary || summary.Role != schema.System {
		return errors.New("compacted context must start with its system summary")
	}
	return dao.saveMessage(ctx, summary, messages)
}

// 普通消息和压缩上下文的差异只留在存储层。
func (dao *ConversationDAO) saveMessage(ctx context.Context, message *messagepkg.Message, messages []*messagepkg.Message) error {
	if message == nil {
		return errors.New("conversation message is required")
	}
	record := &conversationRow{ThreadID: message.ThreadID, MessageID: message.MessageID, RunID: message.RunID, Seq: message.Seq, CreatedAt: message.CreatedAt, Type: "message", Message: message}
	if messages != nil {
		record.Type, record.Message, record.CompactedMessages = "compact", nil, messages
	}
	if dao == nil || dao.Client == nil || dao.Redis == nil {
		return errors.New("conversation persistence requires MySQL and Redis")
	}
	if strings.TrimSpace(message.ThreadID) == "" || strings.TrimSpace(message.MessageID) == "" {
		return errors.New("conversation message requires thread id and message id")
	}
	if record.Seq == 0 {
		sequence, err := cache.GenerateSequence(ctx, dao.Redis, "deepagent:history:seq:thread:"+message.ThreadID)
		if err != nil {
			return err
		}
		record.Seq = sequence
	}
	if record.Seq <= 0 {
		return errors.New("history sequence must be positive")
	}
	database := dao.Client.DB(ctx, true).Table(dao.tableName)
	err := database.Clauses(clause.OnConflict{DoNothing: true}).Create(record).Error
	if err != nil {
		return err
	}
	// 重投时返回原记录的序号，而不是本次分配但未入库的序号。
	err = database.Select("seq").Where("thread_id = ? AND message_id = ?", record.ThreadID, record.MessageID).Scan(&record.Seq).Error
	if err != nil {
		return err
	}
	message.Seq = record.Seq
	return nil
}

// LoadContext 重放数据库记录。压缩替换上下文，但保留被摘要覆盖的消息身份，防止重投。
func (dao *ConversationDAO) LoadContext(ctx context.Context, threadID string) (messages []*messagepkg.Message, messageIDs []string, sequence int64, err error) {
	if dao == nil || dao.Client == nil {
		return nil, nil, 0, errors.New("conversation store is not initialized")
	}
	for {
		var records []*conversationRow
		err := dao.Client.DB(ctx, true).Table(dao.tableName).Where("thread_id = ? AND seq > ?", threadID, sequence).Order("seq ASC").Limit(200).Find(&records).Error
		if err != nil {
			return nil, nil, 0, err
		}
		for _, record := range records {
			if record == nil || record.Seq <= sequence {
				return nil, nil, 0, fmt.Errorf("history sequence must advance past %d", sequence)
			}
			sequence = record.Seq

			messageID := record.MessageID
			messageIDs = append(messageIDs, messageID)
			switch record.Type {
			case "message":
				if record.Message == nil {
					return nil, nil, 0, errors.New("stored conversation message is missing")
				}
				record.Message.MessageID, record.Message.ThreadID, record.Message.RunID, record.Message.Seq, record.Message.CreatedAt = messageID, record.ThreadID, record.RunID, record.Seq, record.CreatedAt
				messages = append(messages, record.Message)
			case "compact":
				if len(record.CompactedMessages) == 0 || record.CompactedMessages[0] == nil || record.CompactedMessages[0].Role != schema.System {
					return nil, nil, 0, errors.New("compact record requires a summary and rebuilt context")
				}
				summary := record.CompactedMessages[0]
				summary.MessageID, summary.ThreadID, summary.RunID, summary.Seq, summary.CreatedAt = messageID, record.ThreadID, record.RunID, record.Seq, record.CreatedAt
				messages = append([]*messagepkg.Message(nil), record.CompactedMessages...)
			default:
				return nil, nil, 0, fmt.Errorf("unknown history record type %q", record.Type)
			}
		}
		if len(records) < 200 {
			return messages, messageIDs, sequence, nil
		}
	}
}
