package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Message = schema.Message

type TokenCounter func(messages []*schema.Message) int

type HistoryRecordType string

const (
	HistoryRecordMessage HistoryRecordType = "message"
	HistoryRecordCompact HistoryRecordType = "compact"
)

type HistoryRecord struct {
	Type       HistoryRecordType
	ThreadID   string
	RunID      string `json:"TurnID" yaml:"turnid"`
	UniqueKey  string
	MessageID  int64
	Seq        int64
	Message    *Message
	CreateAt   int64 // unix timestamp, second
	CreateAtMS int64 // unix timestamp, millisecond
	Ext        *HistoryRecordExtend
}

func (historyRecord *HistoryRecord) GetOrderSequence() int64 {
	if historyRecord == nil {
		return 0
	}
	if historyRecord.Seq > 0 {
		return historyRecord.Seq
	}
	return historyRecord.MessageID
}

type HistoryRecordExtend struct {
	CompactStrategyID      string
	CompactStrategyPayload string
}

type HistoryRolloutStore interface {
	Append(ctx context.Context, rec *HistoryRecord) error
	List(ctx context.Context, q ListQuery) ([]*HistoryRecord, error)
}

type ListOrder string

const (
	ListOrderASC  ListOrder = "asc"
	ListOrderDESC ListOrder = "desc"
)

type ListQuery struct {
	ThreadID string
	RunID    string `json:"TurnID" yaml:"turnid"`
	Order    ListOrder
	Limit    int
	// Seq cursor. Semantics depend on Order:
	// - Order DESC: return records with Seq < BeforeID (older).
	// - Order ASC: return records with Seq > AfterID (newer).
	BeforeID *int64
	AfterID  *int64
}

type HistoryRecordIDProvider func(context.Context, string, string, *schema.Message) int64

type RedisIncrByClient interface {
	IncrBy(ctx context.Context, key string, value int64) (int64, error)
}

type RedisSeqGenerator struct {
	client RedisIncrByClient
	prefix string
}

func NewRedisSeqGenerator(client RedisIncrByClient, prefix string) (generator *RedisSeqGenerator) {
	prefix = strings.Trim(strings.TrimSpace(prefix), ":/")
	if prefix == "" {
		prefix = "agentthread_history_seq"
	}
	return &RedisSeqGenerator{client: client, prefix: prefix}
}

func (sequenceGenerator *RedisSeqGenerator) GenerateSequence(ctx context.Context, threadID string) (sequence int64, err error) {
	if sequenceGenerator == nil || sequenceGenerator.client == nil {
		return 0, errors.New("agentthread: redis seq generator is not initialized")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return 0, errors.New("agentthread: thread id is required to generate history seq")
	}
	return sequenceGenerator.client.IncrBy(ctx, fmt.Sprintf("%s:thread:%s", sequenceGenerator.prefix, threadID), 1)
}

const defaultHistoryTable = "agentthread_history"

// GormHistoryRolloutStore stores HistoryRecord via GORM.
type GormHistoryRolloutStore struct {
	db               *gorm.DB
	table            string
	recordIDProvider func(ctx context.Context, threadID, runID string) int64
	seqGenerator     SeqGenerator
}

type SeqGenerator interface {
	GenerateSequence(ctx context.Context, threadID string) (int64, error)
}

func NewGormHistoryRolloutStore(db *gorm.DB, table string, recordIDProvider func(ctx context.Context, threadID, runID string) int64, seqGenerators ...SeqGenerator) *GormHistoryRolloutStore {
	if table == "" {
		table = defaultHistoryTable
	}
	var seqGenerator SeqGenerator
	if len(seqGenerators) > 0 {
		seqGenerator = seqGenerators[0]
	}
	return &GormHistoryRolloutStore{db: db, table: table, recordIDProvider: recordIDProvider, seqGenerator: seqGenerator}
}

func (historyStore *GormHistoryRolloutStore) MigrateSchema(ctx context.Context) (err error) {
	if historyStore == nil || historyStore.db == nil {
		return errors.New("agentthread: history store not initialized")
	}
	err = historyStore.db.WithContext(ctx).Table(historyStore.table).AutoMigrate(&historyRow{})
	return err
}

func (historyStore *GormHistoryRolloutStore) Append(ctx context.Context, historyRecord *HistoryRecord) error {
	if historyStore == nil || historyStore.db == nil {
		return errors.New("agentthread: history store not initialized")
	}
	if historyRecord == nil {
		return nil
	}
	if historyRecord.MessageID == 0 {
		if historyStore.recordIDProvider == nil {
			return errors.New("agentthread: history record message_id is empty and no store id provider is configured")
		}
		historyRecord.MessageID = historyStore.recordIDProvider(ctx, historyRecord.ThreadID, historyRecord.RunID)
		if historyRecord.MessageID == 0 {
			return errors.New("agentthread: history record id provider returned 0")
		}
	}
	if historyRecord.Seq == 0 {
		if historyStore.seqGenerator != nil {
			sequence, err := historyStore.seqGenerator.GenerateSequence(ctx, historyRecord.ThreadID)
			if err != nil {
				return err
			}
			historyRecord.Seq = sequence
		} else {
			historyRecord.Seq = historyRecord.MessageID
		}
		if historyRecord.Seq <= 0 {
			return errors.New("agentthread: history record seq generator returned non-positive seq")
		}
	}
	row, err := encodeHistoryRow(historyRecord)
	if err != nil {
		return err
	}
	return historyStore.db.WithContext(ctx).Table(historyStore.table).Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
}

func (historyStore *GormHistoryRolloutStore) List(ctx context.Context, query ListQuery) ([]*HistoryRecord, error) {
	if historyStore == nil || historyStore.db == nil {
		return nil, errors.New("agentthread: history store not initialized")
	}
	queryDB := historyStore.db.WithContext(ctx).Table(historyStore.table)
	if query.ThreadID != "" {
		queryDB = queryDB.Where("thread_id = ?", query.ThreadID)
	}
	if query.RunID != "" {
		queryDB = queryDB.Where("turn_id = ?", query.RunID)
	}
	queryDB = queryDB.Where("seq > 0")
	if query.Order == ListOrderDESC {
		if query.BeforeID != nil {
			queryDB = queryDB.Where("seq < ?", *query.BeforeID)
		}
		queryDB = queryDB.Order("seq DESC")
	} else {
		if query.AfterID != nil {
			queryDB = queryDB.Where("seq > ?", *query.AfterID)
		}
		queryDB = queryDB.Order("seq ASC")
	}
	if query.Limit > 0 {
		queryDB = queryDB.Limit(query.Limit)
	}
	var rows []*historyRow
	checkErr := queryDB.Find(&rows).Error
	if checkErr != nil {
		return nil, checkErr
	}
	historyRecords := make([]*HistoryRecord, 0, len(rows))
	for _, row := range rows {
		historyRecord, err := row.decodeHistoryRecord()
		if err != nil {
			return nil, err
		}
		historyRecords = append(historyRecords, historyRecord)
	}
	return historyRecords, nil
}

// historyRow preserves the existing durable rollout schema.
type historyRow struct {
	ThreadID  string `gorm:"column:thread_id;primaryKey;size:128"`
	MessageID int64  `gorm:"column:message_id;primaryKey"`
	Seq       int64  `gorm:"column:seq"`
	RunID     string `gorm:"column:turn_id;size:128" json:"TurnID" yaml:"turnid"`
	Type      string `gorm:"column:type;size:32"`
	Message   string `gorm:"column:message"`
	Ext       string `gorm:"column:ext"`
	CreateAt  int64  `gorm:"column:created_at"`
}

func encodeHistoryRow(historyRecord *HistoryRecord) (*historyRow, error) {
	row := &historyRow{
		ThreadID:  historyRecord.ThreadID,
		MessageID: historyRecord.MessageID,
		Seq:       historyRecord.Seq,
		RunID:     historyRecord.RunID,
		Type:      string(historyRecord.Type),
		CreateAt:  historyRecord.CreateAt,
	}
	if historyRecord.Message != nil {
		encodedJSON, err := json.Marshal(historyRecord.Message)
		if err != nil {
			return nil, err
		}
		row.Message = string(encodedJSON)
	}
	if historyRecord.Ext != nil {
		encodedJSON, err := json.Marshal(historyRecord.Ext)
		if err != nil {
			return nil, err
		}
		row.Ext = string(encodedJSON)
	}
	return row, nil
}

func (historyRow *historyRow) decodeHistoryRecord() (*HistoryRecord, error) {
	historyRecord := &HistoryRecord{
		Type:      HistoryRecordType(historyRow.Type),
		ThreadID:  historyRow.ThreadID,
		RunID:     historyRow.RunID,
		MessageID: historyRow.MessageID,
		Seq:       historyRow.Seq,
		CreateAt:  historyRow.CreateAt,
	}
	if historyRow.Message != "" {
		var message Message
		err := json.Unmarshal([]byte(historyRow.Message), &message)
		if err != nil {
			return nil, err
		}
		historyRecord.Message = &message
	}
	if historyRow.Ext != "" {
		var historyExtension HistoryRecordExtend
		err := json.Unmarshal([]byte(historyRow.Ext), &historyExtension)
		if err != nil {
			return nil, err
		}
		historyRecord.Ext = &historyExtension
	}
	return historyRecord, nil
}
