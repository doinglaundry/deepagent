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

func (r *HistoryRecord) OrderSeq() int64 {
	if r == nil {
		return 0
	}
	if r.Seq > 0 {
		return r.Seq
	}
	return r.MessageID
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

func (g *RedisSeqGenerator) Next(ctx context.Context, threadID string) (sequence int64, err error) {
	if g == nil || g.client == nil {
		return 0, errors.New("agentthread: redis seq generator is not initialized")
	}
	threadID = strings.TrimSpace(threadID)
	if threadID == "" {
		return 0, errors.New("agentthread: thread id is required to generate history seq")
	}
	return g.client.IncrBy(ctx, fmt.Sprintf("%s:thread:%s", g.prefix, threadID), 1)
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
	Next(ctx context.Context, threadID string) (int64, error)
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

func (s *GormHistoryRolloutStore) AutoMigrate(ctx context.Context) (err error) {
	if s == nil || s.db == nil {
		return errors.New("agentthread: history store not initialized")
	}
	err = s.db.WithContext(ctx).Table(s.table).AutoMigrate(&historyRow{})
	return err
}

func (s *GormHistoryRolloutStore) Append(ctx context.Context, rec *HistoryRecord) error {
	if s == nil || s.db == nil {
		return errors.New("agentthread: history store not initialized")
	}
	if rec == nil {
		return nil
	}
	if rec.MessageID == 0 {
		if s.recordIDProvider == nil {
			return errors.New("agentthread: history record message_id is empty and no store id provider is configured")
		}
		rec.MessageID = s.recordIDProvider(ctx, rec.ThreadID, rec.RunID)
		if rec.MessageID == 0 {
			return errors.New("agentthread: history record id provider returned 0")
		}
	}
	if rec.Seq == 0 {
		if s.seqGenerator != nil {
			seq, err := s.seqGenerator.Next(ctx, rec.ThreadID)
			if err != nil {
				return err
			}
			rec.Seq = seq
		} else {
			rec.Seq = rec.MessageID
		}
		if rec.Seq <= 0 {
			return errors.New("agentthread: history record seq generator returned non-positive seq")
		}
	}
	row, err := toHistoryRow(rec)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Table(s.table).Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error
}

func (s *GormHistoryRolloutStore) List(ctx context.Context, q ListQuery) ([]*HistoryRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("agentthread: history store not initialized")
	}
	db := s.db.WithContext(ctx).Table(s.table)
	if q.ThreadID != "" {
		db = db.Where("thread_id = ?", q.ThreadID)
	}
	if q.RunID != "" {
		db = db.Where("turn_id = ?", q.RunID)
	}
	db = db.Where("seq > 0")
	if q.Order == ListOrderDESC {
		if q.BeforeID != nil {
			db = db.Where("seq < ?", *q.BeforeID)
		}
		db = db.Order("seq DESC")
	} else {
		if q.AfterID != nil {
			db = db.Where("seq > ?", *q.AfterID)
		}
		db = db.Order("seq ASC")
	}
	if q.Limit > 0 {
		db = db.Limit(q.Limit)
	}
	var rows []*historyRow
	checkErr := db.Find(&rows).Error
	if checkErr != nil {
		return nil, checkErr
	}
	out := make([]*HistoryRecord, 0, len(rows))
	for _, row := range rows {
		rec, err := row.toRecord()
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, nil
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

func toHistoryRow(rec *HistoryRecord) (*historyRow, error) {
	row := &historyRow{
		ThreadID:  rec.ThreadID,
		MessageID: rec.MessageID,
		Seq:       rec.Seq,
		RunID:     rec.RunID,
		Type:      string(rec.Type),
		CreateAt:  rec.CreateAt,
	}
	if rec.Message != nil {
		b, err := json.Marshal(rec.Message)
		if err != nil {
			return nil, err
		}
		row.Message = string(b)
	}
	if rec.Ext != nil {
		b, err := json.Marshal(rec.Ext)
		if err != nil {
			return nil, err
		}
		row.Ext = string(b)
	}
	return row, nil
}

func (r *historyRow) toRecord() (*HistoryRecord, error) {
	rec := &HistoryRecord{
		Type:      HistoryRecordType(r.Type),
		ThreadID:  r.ThreadID,
		RunID:     r.RunID,
		MessageID: r.MessageID,
		Seq:       r.Seq,
		CreateAt:  r.CreateAt,
	}
	if r.Message != "" {
		var msg Message
		err := json.Unmarshal([]byte(r.Message), &msg)
		if err != nil {
			return nil, err
		}
		rec.Message = &msg
	}
	if r.Ext != "" {
		var ext HistoryRecordExtend
		err := json.Unmarshal([]byte(r.Ext), &ext)
		if err != nil {
			return nil, err
		}
		rec.Ext = &ext
	}
	return rec, nil
}
