package manager

import (
	"context"
	"encoding/json"
	"time"

	"gorm.io/gorm"
)

// MySQLStore is the durable Manager Store. AutoMigrate is intentionally kept
// here so a local worker can bootstrap an empty database without extra SQL.
type MySQLStore struct{ db *gorm.DB }

type threadRow struct {
	ID        string `gorm:"primaryKey;size:128"`
	SessionID string `gorm:"index;size:128"`
	Status    string `gorm:"index;size:32"`
	UpdatedAt time.Time
	Inputs    []byte `gorm:"type:longblob"`
}

func NewMySQLStore(db *gorm.DB) (*MySQLStore, error) {
	s := &MySQLStore{db: db}
	if db == nil {
		return nil, gorm.ErrInvalidDB
	}
	return s, db.AutoMigrate(&threadRow{})
}

func (s *MySQLStore) CreateThread(ctx context.Context, t *Thread) error {
	b, err := json.Marshal(t.Inputs)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Create(&threadRow{ID: t.ID, SessionID: t.SessionID, Status: string(t.Status), UpdatedAt: t.UpdatedAt, Inputs: b}).Error
}
func (s *MySQLStore) GetThread(ctx context.Context, id string) (*Thread, error) {
	var r threadRow
	if err := s.db.WithContext(ctx).First(&r, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return rowThread(r)
}
func (s *MySQLStore) ListThreads(ctx context.Context, sessionID string) ([]*Thread, error) {
	q := s.db.WithContext(ctx).Order("updated_at desc")
	if sessionID != "" {
		q = q.Where("session_id = ?", sessionID)
	}
	var rows []threadRow
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*Thread, 0, len(rows))
	for _, r := range rows {
		t, err := rowThread(r)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}
func (s *MySQLStore) UpdateThread(ctx context.Context, t *Thread) error {
	b, err := json.Marshal(t.Inputs)
	if err != nil {
		return err
	}
	return s.db.WithContext(ctx).Model(&threadRow{}).Where("id = ?", t.ID).Updates(map[string]any{"session_id": t.SessionID, "status": string(t.Status), "updated_at": t.UpdatedAt, "inputs": b}).Error
}
func rowThread(r threadRow) (*Thread, error) {
	var in [][]byte
	if len(r.Inputs) > 0 {
		if err := json.Unmarshal(r.Inputs, &in); err != nil {
			return nil, err
		}
	}
	return &Thread{ID: r.ID, SessionID: r.SessionID, Status: ThreadStatus(r.Status), UpdatedAt: r.UpdatedAt, Inputs: in}, nil
}
