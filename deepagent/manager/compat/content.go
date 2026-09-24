package manager

import (
	"bytes"
	"eino-cli/deepagent/manager/api"
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type inputRow struct {
	Namespace string `gorm:"primaryKey;type:varbinary(191)"`
	ThreadID  string `gorm:"primaryKey;type:varbinary(64)"`
	ID        string `gorm:"primaryKey;type:varbinary(191)"`
	Position  int64
	Body      []byte `gorm:"type:longblob"`
}

func (inputRow) TableName() string { return "deepagent_inputs" }

type historyRow struct {
	Rollout     []byte `gorm:"type:longblob"`
	Namespace   string `gorm:"primaryKey;type:varbinary(191)"`
	ThreadID    string `gorm:"primaryKey;type:varbinary(64)"`
	Version     int64
	Messages    []byte `gorm:"type:longblob"`
	Compactions []byte `gorm:"type:longblob"`
}

func (historyRow) TableName() string { return "deepagent_histories" }

func (s *sqlStore) loadContent(db *gorm.DB, r *record) error {
	var rows []inputRow
	if e := db.Where("namespace = ? AND thread_id = ?", s.namespace, r.Thread.ID).Order("position ASC").Find(&rows).Error; e != nil {
		return e
	}
	// Preserve embedded legacy data until the first locked update migrates it.
	if len(rows) > 0 {
		r.Inputs = make([]delivery, 0, len(rows))
		for _, row := range rows {
			var d delivery
			if e := json.Unmarshal(row.Body, &d); e != nil {
				return e
			}
			r.Inputs = append(r.Inputs, d)
		}
	}
	return s.loadModelHistory(db, r)
}
func (s *sqlStore) loadModelHistory(db *gorm.DB, r *record) error {
	var h historyRow
	e := db.Where("namespace = ? AND thread_id = ?", s.namespace, r.Thread.ID).Take(&h).Error
	if errors.Is(e, gorm.ErrRecordNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	r.History = api.History{Version: h.Version, Messages: h.Messages, Compactions: h.Compactions, Rollout: h.Rollout}
	return nil
}
func (s *sqlStore) saveContent(db *gorm.DB, before, after *record) error {
	previous := map[string][]byte{}
	if before != nil && !before.legacy {
		for _, d := range before.Inputs {
			b, e := json.Marshal(d)
			if e != nil {
				return e
			}
			previous[d.Input.ID] = b
		}
	}
	for i, d := range after.Inputs {
		b, e := json.Marshal(d)
		if e != nil {
			return e
		}
		if bytes.Equal(previous[d.Input.ID], b) {
			continue
		}
		row := inputRow{Namespace: s.namespace, ThreadID: after.Thread.ID, ID: d.Input.ID, Position: int64(i), Body: b}
		if e = db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "namespace"}, {Name: "thread_id"}, {Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"position", "body"})}).Create(&row).Error; e != nil {
			return e
		}
	}
	changed := before == nil || before.legacy || before.History.Version != after.History.Version || !bytes.Equal(before.History.Messages, after.History.Messages) || !bytes.Equal(before.History.Compactions, after.History.Compactions) || !bytes.Equal(before.History.Rollout, after.History.Rollout)
	if changed && after.History.Version > 0 {
		h := after.History
		if e := db.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "namespace"}, {Name: "thread_id"}}, DoUpdates: clause.AssignmentColumns([]string{"version", "messages", "compactions", "rollout"})}).Create(&historyRow{Namespace: s.namespace, ThreadID: after.Thread.ID, Version: h.Version, Messages: h.Messages, Compactions: h.Compactions, Rollout: h.Rollout}).Error; e != nil {
			return e
		}
	}
	return nil
}
