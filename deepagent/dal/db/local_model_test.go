package db

import (
	"context"
	agentmodel "eino-cli/deepagent/model"
	"fmt"
	"github.com/cloudwego/eino/schema"
	"os"
	"testing"
	"time"
)

func TestTrainingExampleSnapshotsAndDeduplicatesContent(t *testing.T) {
	messages := []*agentmodel.Message{agentmodel.NewUserMessage("preference"), agentmodel.NewAssistantMessage("concise Chinese", nil)}
	messages[0].MessageID = "delivery-id"
	messages[0].Extra = map[string]any{"delivery": "metadata"}
	firstExample, err := buildTrainingExample(messages)
	if err != nil {
		t.Fatal(err)
	}
	if firstExample.Messages[0].MessageID != "" || firstExample.Messages[0].Extra != nil {
		t.Fatal("sample retained delivery metadata")
	}
	messages[0].MessageID = "different-delivery"
	duplicateExample, err := buildTrainingExample(messages)
	if err != nil || firstExample.ID != duplicateExample.ID {
		t.Fatalf("dedup failed: %v", err)
	}
	messages[1].Content = "mutated"
	if firstExample.Messages[1].Content != "concise Chinese" {
		t.Fatal("sample shares mutable message")
	}
	for _, invalid := range [][]*agentmodel.Message{
		{nil, messages[1]}, {messages[0]}, {messages[1], messages[0]},
		{messages[0], messages[1], messages[0]},
		{messages[0], agentmodel.NewAssistantMessage("bad", []schema.ToolCall{{ID: "call"}})},
	} {
		_, err := buildTrainingExample(invalid)
		if err == nil {
			t.Fatal("invalid training example accepted")
		}
	}
}

func TestLocalModelDAOJobLifecycle(t *testing.T) {
	mysqlDSN := os.Getenv("DEEPAGENT_LOCAL_MODEL_TEST_DSN")
	if mysqlDSN == "" {
		t.Skip("set dedicated DEEPAGENT_LOCAL_MODEL_TEST_DSN")
	}
	ctx := context.Background()
	mysqlClient, err := NewSQL(ctx, mysqlDSN, "")
	if err != nil {
		t.Fatal(err)
	}
	localModelDAO := NewLocalModelDAO(mysqlClient, "test-personal-model")
	err = localModelDAO.MigrateSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// 仅清理本测试的模型，避免与 Web 验收或其他包并行执行时删除彼此的样本。
	cleanup := func() {
		for _, row := range []any{&agentmodel.TrainingJob{}, &agentmodel.TrainingExample{}} {
			deleteErr := mysqlClient.DB(ctx, true).Where("model_name IN ?", []string{"test-personal-model", "another-personal-model"}).Delete(row).Error
			if deleteErr != nil {
				t.Error(deleteErr)
			}
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	for i := 0; i < 12; i++ {
		_, err = localModelDAO.ConfirmTrainingExample(ctx, []*agentmodel.Message{agentmodel.NewUserMessage(fmt.Sprint("question ", i)), agentmodel.NewAssistantMessage("confirmed", nil)}, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err = localModelDAO.ConfirmTrainingExample(ctx, []*agentmodel.Message{agentmodel.NewUserMessage("question 0"), agentmodel.NewAssistantMessage("confirmed", nil)}, "")
	if err != nil {
		t.Fatal(err)
	}
	newExampleCount, err := localModelDAO.CountNewTrainingExamples(ctx)
	if err != nil || newExampleCount != 12 {
		t.Fatalf("count=%d err=%v", newExampleCount, err)
	}
	trainingJob, err := localModelDAO.CreateTrainingJob(ctx)
	if err != nil || trainingJob.Status != "running" {
		t.Fatalf("new training job=%+v err=%v", trainingJob, err)
	}
	trainingMessages, err := localModelDAO.GetTrainingMessages(ctx, time.Now())
	if err != nil || len(trainingMessages) != 12 || trainingMessages[0][1].Content != "confirmed" {
		t.Fatalf("snapshot read: %v", err)
	}
	trainingJob.Status, trainingJob.FineTunedParametersPath = "pending_validation", "/tmp/candidate"
	err = localModelDAO.SaveTrainingJobResult(ctx, trainingJob)
	if err != nil {
		t.Fatal(err)
	}
	newExampleCount, err = localModelDAO.CountNewTrainingExamples(ctx)
	if err != nil || newExampleCount != 0 {
		t.Fatalf("attempt cursor: count=%d err=%v", newExampleCount, err)
	}
	// Messages confirmed after training starts belong to the next training snapshot.
	laterTrainingExample, err := localModelDAO.ConfirmTrainingExample(ctx, []*agentmodel.Message{agentmodel.NewUserMessage("after request"), agentmodel.NewAssistantMessage("later answer", nil)}, "")
	if err != nil {
		t.Fatal(err)
	}
	err = mysqlClient.DB(ctx, true).Model(laterTrainingExample).Update("created_at", trainingJob.CreatedAt.Add(time.Second)).Error
	if err != nil {
		t.Fatal(err)
	}
	trainingMessages, err = localModelDAO.GetTrainingMessages(ctx, trainingJob.CreatedAt)
	if err != nil || len(trainingMessages) != 12 {
		t.Fatalf("training snapshot changed: conversations=%d err=%v", len(trainingMessages), err)
	}
	newExampleCount, err = localModelDAO.CountNewTrainingExamples(ctx)
	if err != nil || newExampleCount != 1 {
		t.Fatalf("new sample cursor: count=%d err=%v", newExampleCount, err)
	}
	_, err = localModelDAO.CreateTrainingJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	otherLocalModelDAO := NewLocalModelDAO(mysqlClient, "another-personal-model")
	otherTrainingJob, otherErr := otherLocalModelDAO.CreateTrainingJob(ctx)
	if otherErr != nil {
		t.Fatal(otherErr)
	}
	err = localModelDAO.FailAbandonedTrainingJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	otherTrainingJobs, otherErr := otherLocalModelDAO.GetTrainingJobs(ctx)
	if otherErr != nil || len(otherTrainingJobs) != 1 || otherTrainingJobs[0].ID != otherTrainingJob.ID || otherTrainingJobs[0].Status != "running" {
		t.Fatalf("changed another model: %+v %v", otherTrainingJobs, otherErr)
	}
	trainingJobs, err := localModelDAO.GetTrainingJobs(ctx)
	if err != nil || len(trainingJobs) != 2 {
		t.Fatalf("jobs=%+v err=%v", trainingJobs, err)
	}
	for _, trainingJob := range trainingJobs {
		if trainingJob.Status == "running" {
			t.Fatal("abandoned job remains running")
		}
	}
}
