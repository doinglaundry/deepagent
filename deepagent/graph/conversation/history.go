package conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TokenCounter func([]*schema.Message) int
type HistoryRecordType string

const (
	HistoryRecordMessage HistoryRecordType = "message"
	HistoryRecordCompact HistoryRecordType = "compact"
)

// HistoryRecord is both the durable record and its GORM mapping.
type HistoryRecord struct {
	ThreadID          string            `gorm:"column:thread_id;primaryKey;size:128"`
	MessageID         int64             `gorm:"column:message_id;primaryKey;autoIncrement:false"`
	Seq               int64             `gorm:"column:seq"`
	RunID             string            `gorm:"column:turn_id;size:128"`
	Type              HistoryRecordType `gorm:"column:type;size:32"`
	Message           *schema.Message   `gorm:"column:message;type:longtext;serializer:json"`
	CompactedMessages []*schema.Message `gorm:"column:ext;type:longtext;serializer:json"`
	CreatedAt         int64             `gorm:"column:created_at;autoCreateTime:false"`
}

type HistoryStore interface {
	Append(context.Context, *HistoryRecord) error
	LoadAfter(ctx context.Context, threadID string, sequence int64, limit int) ([]*HistoryRecord, error)
}

type HistoryRecordIDProvider func(context.Context, string, string, *schema.Message) (int64, error)
type RedisIncrByClient interface {
	IncrBy(context.Context, string, int64) (int64, error)
}
type RedisSeqGenerator struct {
	client RedisIncrByClient
	prefix string
}

func NewRedisSeqGenerator(client RedisIncrByClient, prefix string) *RedisSeqGenerator {
	prefix = strings.Trim(strings.TrimSpace(prefix), ":/")
	if prefix == "" {
		prefix = "agentthread_history_seq"
	}
	return &RedisSeqGenerator{client: client, prefix: prefix}
}
func (generator *RedisSeqGenerator) GenerateSequence(ctx context.Context, threadID string) (int64, error) {
	if generator == nil || generator.client == nil {
		return 0, errors.New("history sequence generator is not initialized")
	}
	if strings.TrimSpace(threadID) == "" {
		return 0, errors.New("history thread id is required")
	}
	return generator.client.IncrBy(ctx, fmt.Sprintf("%s:thread:%s", generator.prefix, threadID), 1)
}

type SeqGenerator interface {
	GenerateSequence(context.Context, string) (int64, error)
}

type GormHistoryStore struct {
	db           *gorm.DB
	table        string
	seqGenerator SeqGenerator
}

func NewGormHistoryStore(db *gorm.DB, table string, seqGenerator SeqGenerator) *GormHistoryStore {
	if table == "" {
		table = "agentthread_history"
	}
	return &GormHistoryStore{db: db, table: table, seqGenerator: seqGenerator}
}
func (store *GormHistoryStore) MigrateSchema(ctx context.Context) error {
	if store == nil || store.db == nil {
		return errors.New("history store is not initialized")
	}
	database := store.db.WithContext(ctx).Table(store.table)
	err := database.AutoMigrate(&HistoryRecord{})
	if err != nil {
		return err
	}
	// Convert existing compact records once. Normal reads only decode message lists.
	payload := "JSON_UNQUOTE(JSON_EXTRACT(ext, '$.CompactStrategyPayload'))"
	summary := "JSON_EXTRACT(" + payload + ", '$.Summary')"
	retained := "JSON_EXTRACT(" + payload + ", '$.Retained')"
	window := "JSON_MERGE_PRESERVE(JSON_ARRAY(" + summary + "), IF(JSON_TYPE(" + retained + ") = 'ARRAY', " + retained + ", JSON_ARRAY()))"
	return database.Where("type = ? AND JSON_VALID(ext) AND JSON_TYPE(ext) = 'OBJECT'", HistoryRecordCompact).
		Where("JSON_UNQUOTE(JSON_EXTRACT(ext, '$.CompactStrategyID')) = ?", "core_snapshot_v1").
		Where("JSON_EXTRACT("+payload+", '$.Version') = 1 AND JSON_TYPE("+summary+") = 'OBJECT'").
		UpdateColumn("ext", gorm.Expr(window)).Error
}
func (store *GormHistoryStore) Append(ctx context.Context, record *HistoryRecord) error {
	if store == nil || store.db == nil || store.seqGenerator == nil {
		return errors.New("history store requires a database and sequence generator")
	}
	if record == nil || record.ThreadID == "" || record.MessageID <= 0 {
		return errors.New("history record requires a thread id and positive message id")
	}
	if record.Seq == 0 {
		sequence, err := store.seqGenerator.GenerateSequence(ctx, record.ThreadID)
		if err != nil {
			return err
		}
		record.Seq = sequence
	}
	if record.Seq <= 0 {
		return errors.New("history sequence must be positive")
	}
	database := store.db.WithContext(ctx).Table(store.table)
	result := database.Clauses(clause.OnConflict{DoNothing: true}).Create(record)
	if result.Error != nil {
		return result.Error
	}
	// Always use the durable sequence; duplicate row counts depend on the MySQL client configuration.
	return database.Select("seq").Where("thread_id = ? AND message_id = ?", record.ThreadID, record.MessageID).Scan(&record.Seq).Error
}
func (store *GormHistoryStore) LoadAfter(ctx context.Context, threadID string, sequence int64, limit int) ([]*HistoryRecord, error) {
	if store == nil || store.db == nil {
		return nil, errors.New("history store is not initialized")
	}
	var records []*HistoryRecord
	err := store.db.WithContext(ctx).Table(store.table).
		Where("thread_id = ? AND seq > ?", threadID, sequence).
		Order("seq ASC").Limit(limit).Find(&records).Error
	return records, err
}
