package model

import "time"

// TrainingExample is an explicitly confirmed text conversation, independent of compaction.
type TrainingExample struct {
	ModelName string     `gorm:"size:191;primaryKey" json:"model_name"`
	ID        string     `gorm:"size:64;primaryKey" json:"id"`
	Messages  []*Message `gorm:"type:longtext;serializer:coordinator_json" json:"messages"`
	CreatedAt time.Time  `json:"created_at"`
}

func (TrainingExample) TableName() string { return "agent_training_example" }

// TrainingJob 记录一次 MLX 训练；训练完成后等待验证，不会自动启用生成的参数。
type TrainingJob struct {
	ModelName               string    `gorm:"size:191;index" json:"model_name"`
	ID                      string    `gorm:"size:64;primaryKey" json:"id"`
	Status                  string    `gorm:"size:32;index" json:"status"`                       // running 训练中；pending_validation 待验证；failed 失败；canceled 已取消。
	FineTunedParametersPath string    `gorm:"column:adapter_path" json:"adapter_path,omitempty"` // 本次训练生成的候选微调参数目录。
	Error                   string    `gorm:"type:text" json:"error,omitempty"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

func (TrainingJob) TableName() string { return "agent_training_job" }
