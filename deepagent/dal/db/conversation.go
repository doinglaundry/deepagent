package db

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"eino-cli/deepagent/dal/cache"
	agentmodel "eino-cli/deepagent/model"

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
	Message           *agentmodel.Message   `gorm:"column:message;type:longtext;serializer:coordinator_json"`
	CompactedMessages []*agentmodel.Message `gorm:"column:ext;type:longtext;serializer:coordinator_json"`
	CreatedAt         int64                 `gorm:"column:created_at;autoCreateTime:false"`
}

// ConversationDAO uses the same MySQL connection and transaction context as other DAOs.
type ConversationDAO struct {
	Client    *MySQLClient
	Redis     agentmodel.RedisClient
	tableName string
}

func NewConversationDAO(client *MySQLClient, tableName string, redis agentmodel.RedisClient) *ConversationDAO {
	if tableName == "" {
		tableName = "agentthread_history"
	}
	return &ConversationDAO{Client: client, tableName: tableName, Redis: redis}
}

// AppendMessage 保存业务消息，使用 Manager 的 MySQL 连接和事务。
func (dao *ConversationDAO) AppendMessage(ctx context.Context, message *agentmodel.Message) error {
	return dao.saveMessage(ctx, message, nil)
}
func (dao *ConversationDAO) SaveContext(ctx context.Context, messages []*agentmodel.Message) error {
	if len(messages) == 0 || messages[0] == nil || messages[0].Role != schema.System {
		return errors.New("compacted context must start with its system summary")
	}
	summary := messages[0]
	return dao.saveMessage(ctx, summary, messages)
}

// 普通消息和压缩上下文的差异只留在存储层。
func (dao *ConversationDAO) saveMessage(ctx context.Context, message *agentmodel.Message, messages []*agentmodel.Message) error {
	if message == nil {
		return errors.New("conversation message is required")
	}
	record := &conversationRow{
		ThreadID:  message.ThreadID,
		MessageID: message.MessageID,
		RunID:     message.RunID,
		Seq:       message.Seq,
		CreatedAt: message.CreatedAt,
		Type:      "message",
		Message:   message,
	}
	if messages != nil {
		record.Type = "compact"
		record.Message = nil
		record.CompactedMessages = messages
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
func (dao *ConversationDAO) LoadContext(ctx context.Context, threadID string) ([]*agentmodel.Message, []string, int64, error) {
	if dao == nil || dao.Client == nil {
		return nil, nil, 0, errors.New("conversation store is not initialized")
	}
	var messages []*agentmodel.Message
	var recordedMessageIDs []string
	lastReadSeq := int64(0)
	for {
		var records []*conversationRow
		err := dao.Client.DB(ctx, true).
			Table(dao.tableName).
			Where("thread_id = ? AND seq > ?", threadID, lastReadSeq).
			Order("seq ASC").
			Limit(200).
			Find(&records).Error
		if err != nil {
			return nil, nil, 0, err
		}
		for _, record := range records {
			if record == nil || record.Seq <= lastReadSeq {
				return nil, nil, 0, fmt.Errorf("history sequence must advance past %d", lastReadSeq)
			}
			lastReadSeq = record.Seq

			messageID := record.MessageID
			recordedMessageIDs = append(recordedMessageIDs, messageID)
			switch record.Type {
			case "message":
				message := record.Message
				if message == nil {
					return nil, nil, 0, errors.New("stored conversation message is missing")
				}
				message.MessageID = messageID
				message.ThreadID = record.ThreadID
				message.RunID = record.RunID
				message.Seq = record.Seq
				message.CreatedAt = record.CreatedAt
				messages = append(messages, message)
			case "compact":
				if len(record.CompactedMessages) == 0 || record.CompactedMessages[0] == nil || record.CompactedMessages[0].Role != schema.System {
					return nil, nil, 0, errors.New("compact record requires a summary and rebuilt context")
				}
				summary := record.CompactedMessages[0]
				summary.MessageID = messageID
				summary.ThreadID = record.ThreadID
				summary.RunID = record.RunID
				summary.Seq = record.Seq
				summary.CreatedAt = record.CreatedAt
				messages = slices.Clone(record.CompactedMessages)
			default:
				return nil, nil, 0, fmt.Errorf("unknown history record type %q", record.Type)
			}
		}
		if len(records) < 200 {
			return messages, recordedMessageIDs, lastReadSeq, nil
		}
	}
}
