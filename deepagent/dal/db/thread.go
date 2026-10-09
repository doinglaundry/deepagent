package db

import (
	"context"
	"encoding/json"
	"errors"
	"maps"

	agentmodel "eino-cli/deepagent/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrThreadNotClosed = errors.New("only closed threads can be deleted")

type ThreadDAO struct{ Client *MySQLClient }

func (d *ThreadDAO) Create(ctx context.Context, thread *agentmodel.ThreadRecord) error {
	return d.Client.DB(ctx, true).Create(thread).Error
}

func (d *ThreadDAO) Get(ctx context.Context, filter *agentmodel.ThreadFilter) ([]*agentmodel.ThreadRecord, error) {
	if filter == nil || filter.Offset < 0 {
		return nil, errors.New("invalid thread filter")
	}
	var threads []*agentmodel.ThreadRecord
	if filter.ForUpdate {
		if ctx == nil || ctx.Value(d.Client) == nil {
			return nil, errors.New("select for update requires a transaction")
		}
	}
	query := filter.DBFilter(d.Client.DB(ctx, filter.Primary || filter.ForUpdate).Model(&agentmodel.ThreadRecord{}))
	if filter.ForUpdate {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	if filter.Total != nil {
		return nil, query.Count(filter.Total).Error
	}
	err := filter.Page(query).Find(&threads).Error
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(threads))
	runIDs := make([]string, 0, len(threads))
	for _, thread := range threads {
		ids = append(ids, thread.ThreadID)
		runIDs = append(runIDs, thread.LastRunID)
	}
	var runs []*agentmodel.RunRecord
	err = d.Client.DB(ctx, filter.Primary || filter.ForUpdate).Where("run_id IN ?", runIDs).Find(&runs).Error
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*agentmodel.RunRecord, len(runs))
	for _, run := range runs {
		byID[run.RunID] = run
	}
	var counts []struct {
		ThreadID int64
		Count    int64
	}
	err = d.Client.DB(ctx, filter.Primary || filter.ForUpdate).Model(&agentmodel.MailboxMessage{}).Select("thread_id, COUNT(*) AS count").Where("thread_id IN ? AND status = ?", ids, agentmodel.MessageStatusPending).Group("thread_id").Scan(&counts).Error
	if err != nil {
		return nil, err
	}
	pending := make(map[int64]int64, len(counts))
	for _, count := range counts {
		pending[count.ThreadID] = count.Count
	}
	for _, thread := range threads {
		thread.LastRun = byID[thread.LastRunID]
		thread.PendingInputs = pending[thread.ThreadID]
	}
	return threads, nil
}

func (d *ThreadDAO) Update(ctx context.Context, filter *agentmodel.ThreadFilter, values map[string]any) (bool, error) {
	if filter == nil || len(filter.IDs) == 0 || filter.ForUpdate {
		return false, errors.New("invalid thread filter")
	}
	metadata, ok := values["metadata_json"].(map[string]string)
	if ok {
		values = maps.Clone(values)
		encoded, err := json.Marshal(metadata)
		if err != nil {
			return false, err
		}
		values["metadata_json"] = string(encoded)
	}
	result := filter.DBFilter(d.Client.DB(ctx, true).Model(&agentmodel.ThreadRecord{})).Updates(values)
	if result.Error != nil {
		return false, result.Error
	}
	return result.RowsAffected > 0, nil
}

// Delete removes a closed thread and its mailbox in one transaction.
func (d *ThreadDAO) Delete(ctx context.Context, id int64) error {
	return d.Client.DB(ctx, true).Transaction(func(tx *gorm.DB) error {
		var thread agentmodel.ThreadRecord
		err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("thread_id = ?", id).Take(&thread).Error
		if err != nil {
			return err
		}
		if thread.Status != agentmodel.ThreadStatusClosed {
			return ErrThreadNotClosed
		}
		err = tx.Where("thread_id = ?", id).Delete(&agentmodel.MailboxMessage{}).Error
		if err != nil {
			return err
		}
		err = tx.Where("thread_id = ?", id).Delete(&agentmodel.RunRecord{}).Error
		if err != nil {
			return err
		}
		return tx.Delete(&thread).Error
	})
}
