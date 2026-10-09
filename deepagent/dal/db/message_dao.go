package db

import (
	"context"
	"errors"

	agentmodel "eino-cli/deepagent/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type MessageDAO struct{ Client *MySQLClient }

func (d *MessageDAO) Create(ctx context.Context, row *agentmodel.MailboxMessage) error {
	if row == nil {
		return errors.New("message is required")
	}
	conflict := clause.OnConflict{DoNothing: true}
	if row.OutputKey != nil {
		conflict = clause.OnConflict{Columns: []clause.Column{{Name: "thread_id"}, {Name: "output_key"}}, DoUpdates: clause.AssignmentColumns([]string{"payload", "metadata_json", "status", "trigger_turn_id"})}
	}
	return d.Client.DB(ctx, true).Table(new(agentmodel.MailboxMessage).TableName()).Clauses(conflict).Create(row).Error
}

func (d *MessageDAO) Get(ctx context.Context, filter *agentmodel.MailboxMessageFilter) ([]*agentmodel.MailboxMessage, error) {
	if filter == nil || filter.Offset < 0 {
		return nil, MySQLErrInvalidFilter
	}
	query := filter.DBFilter(d.Client.DB(ctx, filter.Primary).Table(new(agentmodel.MailboxMessage).TableName()))
	if filter.Total != nil {
		return nil, query.Count(filter.Total).Error
	}
	query = filter.Page(query)
	var rows []*agentmodel.MailboxMessage
	var err error
	if filter.Take {
		err = query.Take(&rows).Error
	} else {
		err = query.Find(&rows).Error
	}
	if err != nil {
		return nil, err
	}
	if !filter.SkipNormalize {
		for _, row := range rows {
			err = row.Normalize()
			if err != nil {
				return nil, err
			}
		}
	}
	return rows, nil
}

func (d *MessageDAO) Update(ctx context.Context, filter *agentmodel.MailboxMessageFilter, values map[string]any) (int64, error) {
	if filter == nil || len(filter.ThreadIDs) == 0 {
		return 0, MySQLErrInvalidFilter
	}
	query := filter.DBFilter(d.Client.DB(ctx, true).Table(new(agentmodel.MailboxMessage).TableName()))
	if filter.IDs == nil {
		query = query.Where("message_id IN ?", []int64{})
	}
	result := query.Updates(values)
	return result.RowsAffected, result.Error
}

func (d *MessageDAO) Delete(ctx context.Context, id int64) error {
	result := d.Client.DB(ctx, true).Where("message_id = ?", id).Delete(&agentmodel.MailboxMessage{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return gorm.ErrRecordNotFound
	}
	return nil
}
