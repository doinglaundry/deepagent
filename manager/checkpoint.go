package manager

import (
	"context"
	"eino-cli/manager/api"
	"fmt"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type fencedCheckpointBackend interface {
	putThreadCheckpoint(context.Context, api.Permit, string, []byte) error
}

var _ api.FencedCheckpointStore = (*Manager)(nil)
var _ api.FencedCheckpointStore = (*Memory)(nil)

func (m *engine) PutThreadCheckpoint(ctx context.Context, p api.Permit, key string, data []byte) error {
	if key == "" || len(key) > 191 {
		return fmt.Errorf("checkpoint key must contain 1-191 bytes")
	}
	return m.store.(fencedCheckpointBackend).putThreadCheckpoint(ctx, p, key, data)
}
func (s *memoryStore) putThreadCheckpoint(ctx context.Context, p api.Permit, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.check(ctx); e != nil {
		return e
	}
	r, ok := s.threads[p.ThreadID]
	if !ok {
		return api.ErrNotFound
	}
	if e := validPermit(cloneRecord(r), p); e != nil {
		return e
	}
	s.checkpoints[key] = append([]byte(nil), data...)
	return nil
}
func (s *sqlStore) putThreadCheckpoint(ctx context.Context, p api.Permit, key string, data []byte) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row threadRow
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("namespace = ? AND id = ?", s.namespace, p.ThreadID).Take(&row).Error; e != nil {
			return mapError(e)
		}
		r, e := decode(row)
		if e != nil {
			return e
		}
		r.now, e = dbNow(tx)
		if e != nil {
			return e
		}
		if e = validPermit(r, p); e != nil {
			return e
		}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "namespace"}, {Name: "key"}}, DoUpdates: clause.AssignmentColumns([]string{"body"})}).Create(&checkpointRow{Namespace: s.namespace, Key: key, Body: data}).Error
	})
}
