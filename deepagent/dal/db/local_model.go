package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"time"

	agentmodel "eino-cli/deepagent/model"

	"github.com/google/uuid"
	"gorm.io/gorm"
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

// ConfirmTrainingExample 保存训练副本，并记住它来自哪条已持久化回复。
func (localModelDAO *LocalModelDAO) ConfirmTrainingExample(ctx context.Context, messages []*agentmodel.Message, sourceMessageID string) (*agentmodel.TrainingExample, error) {
	trainingExample, err := buildTrainingExample(messages)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTrainingExample, err)
	}
	trainingExample.ModelName = localModelDAO.modelName
	err = localModelDAO.mysqlClient.Transaction(ctx, func(ctx context.Context) error {
		connection := localModelDAO.mysqlClient.DB(ctx, true)
		if sourceMessageID != "" {
			// 同一回复的标记和取消串行；不同回复仍能共享同一份去重样本。
			var source agentmodel.MailboxMessage
			lockErr := connection.Clauses(clause.Locking{Strength: "UPDATE"}).Where("message_id = ?", sourceMessageID).Take(&source).Error
			if lockErr != nil {
				return lockErr
			}
			var marked agentmodel.TrainingExample
			sourceJSON, _ := json.Marshal(sourceMessageID)
			findErr := connection.Where("model_name = ? AND JSON_CONTAINS(source_message_ids, ?)", localModelDAO.modelName, string(sourceJSON)).Take(&marked).Error
			if findErr == nil {
				if marked.ID != trainingExample.ID {
					return fmt.Errorf("%w: remove the existing mark before changing it", ErrInvalidTrainingExample)
				}
				trainingExample = &marked
				return nil
			}
			if !errors.Is(findErr, gorm.ErrRecordNotFound) {
				return findErr
			}
		}
		createErr := connection.Clauses(clause.OnConflict{DoNothing: true}).Create(trainingExample).Error
		if createErr != nil {
			return createErr
		}
		findErr := connection.Clauses(clause.Locking{Strength: "UPDATE"}).Where("model_name = ? AND id = ?", localModelDAO.modelName, trainingExample.ID).Take(trainingExample).Error
		if findErr != nil || sourceMessageID == "" {
			return findErr
		}
		trainingExample.SourceMessageIDs = append(trainingExample.SourceMessageIDs, sourceMessageID)
		return connection.Model(trainingExample).Select("SourceMessageIDs").Updates(trainingExample).Error
	})
	return trainingExample, err
}

func (localModelDAO *LocalModelDAO) GetTrainingExamples(ctx context.Context) ([]agentmodel.TrainingExample, error) {
	examples := []agentmodel.TrainingExample{}
	err := localModelDAO.mysqlClient.DB(ctx, true).Where("model_name = ?", localModelDAO.modelName).Order("created_at DESC, id ASC").Find(&examples).Error
	return examples, err
}

// RemoveTrainingExample 只取消当前消息的标记；最后一个来源取消后才删除样本。
func (localModelDAO *LocalModelDAO) RemoveTrainingExample(ctx context.Context, threadID int64, sourceMessageID string) error {
	return localModelDAO.mysqlClient.Transaction(ctx, func(ctx context.Context) error {
		connection := localModelDAO.mysqlClient.DB(ctx, true)
		var source agentmodel.MailboxMessage
		err := connection.Clauses(clause.Locking{Strength: "UPDATE"}).Where("thread_id = ? AND message_id = ?", threadID, sourceMessageID).Take(&source).Error
		if err != nil {
			return err
		}
		var example agentmodel.TrainingExample
		sourceJSON, _ := json.Marshal(sourceMessageID)
		err = connection.Clauses(clause.Locking{Strength: "UPDATE"}).Where("model_name = ? AND JSON_CONTAINS(source_message_ids, ?)", localModelDAO.modelName, string(sourceJSON)).Take(&example).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		example.SourceMessageIDs = slices.DeleteFunc(example.SourceMessageIDs, func(id string) bool { return id == sourceMessageID })
		if len(example.SourceMessageIDs) == 0 {
			return connection.Delete(&example).Error
		}
		return connection.Model(&example).Select("SourceMessageIDs").Updates(&example).Error
	})
}

// GetTrainingConversation 从原始收发记录读问答，不读压缩后的上下文，也不接受页面拼出的消息。
func (localModelDAO *LocalModelDAO) GetTrainingConversation(ctx context.Context, threadID, messageID int64, includePrevious bool) ([]*agentmodel.Message, error) {
	connection := localModelDAO.mysqlClient.DB(ctx, true)
	var reply agentmodel.MailboxMessage
	err := connection.Where("thread_id = ? AND message_id = ? AND message_type = ?", threadID, messageID, "assistant").Take(&reply).Error
	if err != nil {
		return nil, err
	}
	var completedRunCount int64
	err = connection.Model(&agentmodel.RunRecord{}).Where("thread_id = ? AND run_id = ? AND status = ?", threadID, reply.TriggerRunID, "finished").Count(&completedRunCount).Error
	if err != nil {
		return nil, err
	}
	if completedRunCount != 1 {
		return nil, fmt.Errorf("%w: reply must belong to a completed run", ErrInvalidTrainingExample)
	}
	var payload agentmodel.MessageEventPayload
	err = json.Unmarshal(reply.Payload, &payload)
	if err != nil {
		return nil, err
	}
	answerParts := []string{}
	for _, part := range payload.Parts {
		if part.Type != "text" {
			return nil, fmt.Errorf("%w: only text replies can be selected", ErrInvalidTrainingExample)
		}
		answerParts = append(answerParts, part.Text)
	}
	var inputs []agentmodel.MailboxMessage
	err = connection.Where("thread_id = ? AND message_id IN ? AND message_id < ?", threadID, payload.ConsumedMessageIDs, messageID).Order("message_id ASC").Find(&inputs).Error
	if err != nil {
		return nil, err
	}
	if len(inputs) == 0 || len(inputs) != len(payload.ConsumedMessageIDs) {
		return nil, fmt.Errorf("%w: original question is unavailable", ErrInvalidTrainingExample)
	}
	questionParts := []string{}
	for _, input := range inputs {
		if input.MessageType != "input" || input.Status == "canceled" {
			return nil, fmt.Errorf("%w: select an ordinary text conversation", ErrInvalidTrainingExample)
		}
		var question agentmodel.UserMessage
		err = json.Unmarshal(input.Payload, &question)
		if err != nil {
			return nil, err
		}
		for _, part := range question.Parts {
			if part.Type != "text" {
				return nil, fmt.Errorf("%w: only text questions can be selected", ErrInvalidTrainingExample)
			}
			questionParts = append(questionParts, part.Text)
		}
	}
	messages := []*agentmodel.Message{agentmodel.NewUserMessage(strings.Join(questionParts, "\n\n")), agentmodel.NewAssistantMessage(strings.Join(answerParts, "\n"), nil)}
	_, err = buildTrainingExample(messages)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidTrainingExample, err)
	}
	if !includePrevious {
		return messages, nil
	}
	// 只补一轮前文；它必须位于本轮问题之前，避免把工具执行中的发言当成前一轮。
	var previousReply agentmodel.MailboxMessage
	finishedRuns := connection.Model(&agentmodel.RunRecord{}).Select("run_id").Where("thread_id = ? AND status = ?", threadID, "finished")
	err = connection.Where("thread_id = ? AND message_type = ? AND message_id < ? AND trigger_turn_id IN (?)", threadID, "assistant", inputs[0].MessageID, finishedRuns).Order("message_id DESC").Take(&previousReply).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return messages, nil
	}
	if err != nil {
		return nil, err
	}
	previousMessages, err := localModelDAO.GetTrainingConversation(ctx, threadID, previousReply.MessageID, false)
	if err != nil {
		return nil, err
	}
	return append(previousMessages, messages...), nil
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
