package db

import (
	"context"
	"encoding/json"
	"errors"
	"maps"

	"eino-cli/deepagent/dal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrThreadNotClosed = errors.New("only closed threads can be deleted")

type ThreadDAO struct{ Client *MySQLClient }

func (d *ThreadDAO) Create(ctx context.Context, thread *model.Thread) error {
	return d.Client.DB(ctx, true).Omit("ReadyUntil").Create(thread).Error
}

func (d *ThreadDAO) Get(ctx context.Context, filter *model.ThreadFilter) ([]*model.Thread, error) {
	if filter == nil || filter.Offset < 0 {
		return nil, errors.New("invalid thread filter")
	}
	var threads []*model.Thread
	if filter.ForUpdate {
		if ctx == nil || ctx.Value(d.Client) == nil {
			return nil, errors.New("select for update requires a transaction")
		}
	}
	query := filter.DBFilter(d.Client.DB(ctx, filter.Primary || filter.ForUpdate).Model(&model.Thread{}))
	if filter.ForUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if filter.Total != nil {
		return nil, query.Count(filter.Total).Error
	}
	err := filter.Page(query).Find(&threads).Error
	return threads, err
}

func (d *ThreadDAO) Update(ctx context.Context, filter *model.ThreadFilter, values map[string]any) (bool, error) {
	if filter == nil || len(filter.IDs) == 0 || filter.ForUpdate {
		return false, errors.New("invalid thread filter")
	}
	if metadata, ok := values["metadata_json"].(map[string]string); ok {
		values = maps.Clone(values)
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return false, err
		}
		values["metadata_json"] = string(encoded)
	}
	result := filter.DBFilter(d.Client.DB(ctx, true).Model(&model.Thread{})).Updates(values)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// Delete removes a closed thread and its mailbox in one transaction.
func (d *ThreadDAO) Delete(ctx context.Context, id int64) error {
	return d.Client.DB(ctx, true).Transaction(func(tx *gorm.DB) error {
		var thread model.Thread
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("thread_id = ?", id).Take(&thread).Error; err != nil {
			return err
		}
		if thread.Status != model.ThreadStatusClosed {
			return ErrThreadNotClosed
		}
		if err := tx.Where("thread_id = ?", id).Delete(&Message{}).Error; err != nil {
			return err
		}
		return tx.Delete(&thread).Error
	})
}
