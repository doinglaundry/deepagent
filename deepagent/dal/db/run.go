package db

import (
	"context"
	"fmt"

	agentmodel "eino-cli/deepagent/model"

	"gorm.io/gorm/clause"
)

type RunDAO struct{ Client *MySQLClient }

func (d *RunDAO) Get(ctx context.Context, threadID int64, ids []string) (map[string]*agentmodel.RunRecord, error) {
	var rows []*agentmodel.RunRecord
	err := d.Client.DB(ctx, true).Where("thread_id = ? AND run_id IN ?", threadID, ids).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	runs := make(map[string]*agentmodel.RunRecord, len(rows))
	for _, run := range rows {
		runs[run.RunID] = run
	}
	return runs, nil
}

func (d *RunDAO) Save(ctx context.Context, run *agentmodel.RunRecord) error {
	conn := d.Client.DB(ctx, true)
	err := conn.Clauses(clause.OnConflict{DoNothing: true}).Create(run).Error
	if err != nil {
		return err
	}
	var stored agentmodel.RunRecord
	err = conn.Where("run_id = ?", run.RunID).Take(&stored).Error
	if err != nil {
		return err
	}
	if stored.ThreadID != run.ThreadID {
		return fmt.Errorf("Run %q belongs to another Thread", run.RunID)
	}
	return conn.Model(&agentmodel.RunRecord{}).Where("run_id = ? AND thread_id = ?", run.RunID, run.ThreadID).Updates(map[string]any{
		"status": run.Status, "lease_token": run.LeaseToken,
		"interrupt_id": run.InterruptID, "checkpoint_id": run.CheckpointID,
	}).Error
}

func (d *RunDAO) InterruptStarted(ctx context.Context, threadID int64, resumed []string) error {
	query := d.Client.DB(ctx, true).Model(&agentmodel.RunRecord{}).Where("thread_id = ? AND status = ?", threadID, "started")
	if len(resumed) > 0 {
		query = query.Where("run_id NOT IN ?", resumed)
	}
	return query.Update("status", "interrupted").Error
}
