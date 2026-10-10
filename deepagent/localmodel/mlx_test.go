package localmodel

import (
	"context"
	daldb "eino-cli/deepagent/dal/db"
	agentmodel "eino-cli/deepagent/model"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 实机验收：确认一百条样本后自动训练，期间用户任务不等待、不打断训练。
func TestMLXIntegration(t *testing.T) {
	baseModelParametersPath, pythonExecutablePath, mysqlDSN := os.Getenv("DEEPAGENT_MLX_TEST_MODEL"), os.Getenv("DEEPAGENT_MLX_TEST_PYTHON"), os.Getenv("DEEPAGENT_MLX_TEST_DSN")
	if baseModelParametersPath == "" || pythonExecutablePath == "" || mysqlDSN == "" {
		t.Skip("set the three DEEPAGENT_MLX_TEST_* values for real Metal validation")
	}
	ctx := context.Background()
	if !isConnectedToACPower(ctx) {
		t.Skip("automatic training requires AC power")
	}
	mysqlClient, err := daldb.NewSQL(ctx, mysqlDSN, "")
	if err != nil {
		t.Fatal(err)
	}
	localModelDAO := daldb.NewLocalModelDAO(mysqlClient, "metal-integration")
	err = localModelDAO.MigrateSchema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	inferencePort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	config := Config{ModelName: "metal-integration", BaseModelParametersPath: baseModelParametersPath, DataDirectory: t.TempDir(), PythonExecutablePath: pythonExecutablePath, InferencePort: inferencePort, TrainingIterations: 1, EnableAutoTraining: true}
	service, err := New(ctx, config, localModelDAO)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	baseParametersFingerprint := service.GetParametersFingerprint()
	// Synthetic examples validate mechanics, not personalization quality.
	for i := 0; i < 100; i++ {
		_, err = localModelDAO.ConfirmTrainingExample(ctx, []*agentmodel.Message{
			agentmodel.NewUserMessage(fmt.Sprintf("How should you explain code task %d to me?", i)), agentmodel.NewAssistantMessage("Use concise Chinese and show readable Go code first.", nil),
		}, "")
		if err != nil {
			t.Fatal(err)
		}
	}
	finishThread := service.BeginThread()
	answer, err := service.Generate(ctx, "Reply with exactly: LOCAL_MODEL_READY")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real local model answer: %s", answer)
	finishThread()
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	_, err = service.Generate(canceledCtx, "must not run")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("service hid cancellation: %v", err)
	}
	// 只推进测试中的空闲时间，由真实调度循环检查条件并启动训练。
	service.threadActivityMutex.Lock()
	service.idleStartedAt = time.Now().Add(-11 * time.Minute)
	service.threadActivityMutex.Unlock()
	waitForTrainingJobStatus := func(want string) agentmodel.TrainingJob {
		t.Helper()
		deadline := time.Now().Add(3 * time.Minute)
		for time.Now().Before(deadline) {
			trainingJobs, err := localModelDAO.GetTrainingJobs(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(trainingJobs) > 0 {
				if trainingJobs[0].Status == want {
					return trainingJobs[0]
				}
				if trainingJobs[0].Status == "failed" {
					t.Fatalf("MLX training failed: %s", trainingJobs[0].Error)
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", want)
		return agentmodel.TrainingJob{}
	}
	runningTrainingJob := waitForTrainingJobStatus("running")
	trainingLogPath := filepath.Join(config.DataDirectory, "jobs", runningTrainingJob.ID, "train.log")
	deadline := time.Now().Add(time.Minute)
	trainingProcessStarted := false
	for time.Now().Before(deadline) {
		data, readErr := os.ReadFile(trainingLogPath)
		if readErr == nil && strings.Contains(string(data), "Loading pretrained model") {
			trainingProcessStarted = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !trainingProcessStarted {
		t.Fatal("real training process did not start")
	}
	finishThreadDuringTraining := service.BeginThread()
	busyRequestContext, cancelBusyRequest := context.WithTimeout(ctx, time.Second)
	_, busyErr := service.Generate(busyRequestContext, "do not wait for training")
	cancelBusyRequest()
	if !errors.Is(busyErr, ErrModelBusy) {
		finishThreadDuringTraining()
		t.Fatalf("local inference did not return busy during training: %v", busyErr)
	}
	pendingValidationTrainingJob := waitForTrainingJobStatus("pending_validation")
	finishThreadDuringTraining()
	t.Log("user task left automatic training running; local inference returned busy immediately")
	trainingOutput, err := os.ReadFile(filepath.Join(config.DataDirectory, "jobs", pendingValidationTrainingJob.ID, "train.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("real QLoRA output:\n%s", trainingOutput)
	fineTunedParametersFileInfo, err := os.Stat(filepath.Join(pendingValidationTrainingJob.FineTunedParametersPath, "adapters.safetensors"))
	if err != nil || fineTunedParametersFileInfo.Size() == 0 {
		t.Fatalf("no real adapter: %v", err)
	}
	answer, err = service.Generate(ctx, "Reply with exactly: LOCAL_MODEL_STILL_READY")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("after real training, active base model still works: %s; candidate bytes=%d", answer, fineTunedParametersFileInfo.Size())
	err = service.Close()
	if err != nil {
		t.Fatal(err)
	}
	// Explicitly load the candidate in a new service; never replace a running model.
	candidateService, err := New(ctx, Config{ModelName: "metal-integration", BaseModelParametersPath: baseModelParametersPath, FineTunedParametersPath: pendingValidationTrainingJob.FineTunedParametersPath, DataDirectory: t.TempDir(), PythonExecutablePath: pythonExecutablePath, InferencePort: inferencePort, TrainingIterations: 1}, localModelDAO)
	if err != nil {
		t.Fatal(err)
	}
	defer candidateService.Close()
	if candidateService.GetParametersFingerprint() == baseParametersFingerprint {
		t.Fatal("adapter did not change the model version")
	}
	answer, err = candidateService.Generate(ctx, "Reply with exactly: LOCAL_ADAPTER_READY")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("explicit candidate adapter load: %s", answer)
}
