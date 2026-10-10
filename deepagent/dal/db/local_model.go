package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	agentmodel "eino-cli/deepagent/model"

	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

var ErrInvalidTrainingExample = errors.New("invalid training example")

// LocalModelDAO uses the same MySQL client and JSON serializer as Manager.
type LocalModelDAO struct {
	mysqlClient *MySQLClient
	modelName   string
}

func NewLocalModelDAO(mysqlClient *MySQLClient, modelName string) *LocalModelDAO {
	return &LocalModelDAO{mysqlClient: mysqlClient, modelName: modelName}
}

func (localModelDAO *LocalModelDAO) MigrateSchema(ctx context.Context) error {
	if strings.TrimSpace(localModelDAO.modelName) == "" {
		return errors.New("local_model.name is required")
	}
	return localModelDAO.mysqlClient.DB(ctx, true).AutoMigrate(&agentmodel.TrainingExample{}, &agentmodel.TrainingJob{})
}

func (localModelDAO *LocalModelDAO) ConfirmTrainingExample(ctx context.Context, messages []*agentmodel.Message) (*agentmodel.TrainingExample, error) {
	trainingExample, err := buildTrainingExample(messages)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTrainingExample, err)
	}
	trainingExample.ModelName = localModelDAO.modelName
	err = localModelDAO.mysqlClient.DB(ctx, true).Clauses(clause.OnConflict{DoNothing: true}).Create(trainingExample).Error
	return trainingExample, err
}

// Only text SFT is supported. Keep role/content snapshots, never train on tool output or metadata.
func buildTrainingExample(messages []*agentmodel.Message) (*agentmodel.TrainingExample, error) {
	if len(messages) < 2 {
		return nil, errors.New("a confirmed user question and assistant answer are required")
	}
	textMessages := make([]*agentmodel.Message, 0, len(messages))
	expectedRole := "user"
	for i, message := range messages {
		if message == nil || strings.TrimSpace(message.Content) == "" || len(message.ToolCalls) != 0 || message.ToolCallID != "" || len(message.MultiContent) != 0 || len(message.UserInputMultiContent) != 0 || len(message.AssistantGenMultiContent) != 0 {
			return nil, errors.New("training examples must contain nonempty text messages without tool calls")
		}
		if i == 0 && message.Role == "system" {
			textMessages = append(textMessages, agentmodel.NewSystemMessage(message.Content))
			continue
		}
		if string(message.Role) != expectedRole {
			return nil, fmt.Errorf("expected %s message", expectedRole)
		}
		textMessages = append(textMessages, &agentmodel.Message{Role: message.Role, Content: message.Content})
		if expectedRole == "user" {
			expectedRole = "assistant"
		} else {
			expectedRole = "user"
		}
	}
	if expectedRole != "user" {
		return nil, errors.New("the last message must be a confirmed assistant answer")
	}
	messagesJSON, err := json.Marshal(textMessages)
	if err != nil {
		return nil, err
	}
	messageContentHash := sha256.Sum256(messagesJSON)
	return &agentmodel.TrainingExample{ID: hex.EncodeToString(messageContentHash[:]), Messages: textMessages, CreatedAt: time.Now()}, nil
}

// CreateTrainingJob 记录已经满足自动训练条件的任务，不提供排队或手动触发状态。
func (localModelDAO *LocalModelDAO) CreateTrainingJob(ctx context.Context) (*agentmodel.TrainingJob, error) {
	trainingJob := &agentmodel.TrainingJob{ModelName: localModelDAO.modelName, ID: uuid.NewString(), Status: "running", CreatedAt: time.Now()}
	err := localModelDAO.mysqlClient.DB(ctx, true).Create(trainingJob).Error
	return trainingJob, err
}

func (localModelDAO *LocalModelDAO) GetTrainingJobs(ctx context.Context) ([]agentmodel.TrainingJob, error) {
	var trainingJobs []agentmodel.TrainingJob
	err := localModelDAO.mysqlClient.DB(ctx, true).Where("model_name = ?", localModelDAO.modelName).Order("created_at DESC").Limit(20).Find(&trainingJobs).Error
	return trainingJobs, err
}

// Count since the last attempt, including canceled/failed jobs; do not silently retry expensive training.
func (localModelDAO *LocalModelDAO) CountNewTrainingExamples(ctx context.Context) (int64, error) {
	var newExampleCount int64
	latestTrainingTimeQuery := localModelDAO.mysqlClient.DB(ctx, true).Where("model_name = ?", localModelDAO.modelName).Model(&agentmodel.TrainingJob{}).Select("COALESCE(MAX(created_at), '1970-01-01')")
	err := localModelDAO.mysqlClient.DB(ctx, true).Where("model_name = ?", localModelDAO.modelName).Model(&agentmodel.TrainingExample{}).Where("created_at > (?)", latestTrainingTimeQuery).Count(&newExampleCount).Error
	return newExampleCount, err
}

// GetTrainingMessages 读取截止时间之前确认的消息；每一项保留一段完整对话。
func (localModelDAO *LocalModelDAO) GetTrainingMessages(ctx context.Context, confirmedAtOrBefore time.Time) ([][]*agentmodel.Message, error) {
	var trainingExamples []agentmodel.TrainingExample
	err := localModelDAO.mysqlClient.DB(ctx, true).Select("messages").Where("model_name = ?", localModelDAO.modelName).Where("created_at <= ?", confirmedAtOrBefore).Order("id ASC").Find(&trainingExamples).Error
	if err != nil {
		return nil, err
	}
	trainingMessages := make([][]*agentmodel.Message, len(trainingExamples))
	for index, trainingExample := range trainingExamples {
		trainingMessages[index] = trainingExample.Messages
	}
	return trainingMessages, nil
}

func (localModelDAO *LocalModelDAO) SaveTrainingJobResult(ctx context.Context, trainingJob *agentmodel.TrainingJob) error {
	return localModelDAO.mysqlClient.DB(ctx, true).Where("model_name = ?", localModelDAO.modelName).Model(trainingJob).Updates(map[string]any{"status": trainingJob.Status, "adapter_path": trainingJob.FineTunedParametersPath, "error": trainingJob.Error}).Error
}

// The caller owns the exclusive Mac device lock; a leftover running job lost its process.
func (localModelDAO *LocalModelDAO) FailAbandonedTrainingJobs(ctx context.Context) error {
	return localModelDAO.mysqlClient.DB(ctx, true).Where("model_name = ?", localModelDAO.modelName).Model(&agentmodel.TrainingJob{}).Where("status = ?", "running").Updates(map[string]any{"status": "failed", "error": "worker stopped during training; waiting for new confirmed examples"}).Error
}
