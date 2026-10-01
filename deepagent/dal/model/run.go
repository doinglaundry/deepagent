package model

// RunRecord is the durable execution result shared by scheduling and history.
// Eino checkpoint state remains responsible for restoring graph execution.
type RunRecord struct {
	RunID        string `gorm:"column:run_id;size:191;primaryKey"`
	ThreadID     int64  `gorm:"column:thread_id;index"`
	Status       string `gorm:"column:status;size:32"`
	LeaseToken   string `gorm:"column:lease_token;size:191" json:"-"`
	InterruptID  string `gorm:"column:interrupt_id;size:191"`
	CheckpointID string `gorm:"column:checkpoint_id"`
}

func (RunRecord) TableName() string { return "agent_run" }

func (r *RunRecord) Ended() bool {
	return r != nil && (r.Status == "finished" || r.Status == "interrupted" || r.Status == "failed")
}
