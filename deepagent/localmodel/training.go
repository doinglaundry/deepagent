package localmodel

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	agentmodel "eino-cli/deepagent/model"
)

func (service *Service) runTrainingLoop() {
	defer close(service.trainingLoopDone)
	if service.localModelDAO == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-service.serviceContext.Done():
			return
		case <-ticker.C:
			err := service.runAutomaticTraining()
			if err != nil && service.serviceContext.Err() == nil {
				slog.Error("local model training", "error", err)
			}
		}
	}
}

// runAutomaticTraining 只在启用训练、空闲十分钟、接电且新增一百条确认样本后开始训练。
func (service *Service) runAutomaticTraining() error {
	// 1. 检查训练开关、任务活动和供电状态；加锁读取任务数和空闲时间。
	if !service.config.EnableAutoTraining {
		return nil
	}
	service.threadActivityMutex.Lock()
	activeThreadCount := service.activeThreadCount
	idleDuration := time.Since(service.idleStartedAt)
	service.threadActivityMutex.Unlock()
	if activeThreadCount != 0 || idleDuration < 10*time.Minute || !isConnectedToACPower(service.serviceContext) {
		return nil
	}

	// 2. 统计上次训练尝试之后新增的确认样本，不足一百条就继续等待。
	newExampleCount, err := service.localModelDAO.CountNewTrainingExamples(service.serviceContext)
	if err != nil || newExampleCount < 100 {
		return err
	}

	// 3. 创建 running 记录；创建时间也是本次训练读取样本的截止时间。
	trainingJob, err := service.localModelDAO.CreateTrainingJob(service.serviceContext)
	if err != nil {
		return err
	}

	// 4. 执行微调，最多三十分钟；新用户任务不会取消训练。
	trainingContext, cancelTraining := context.WithTimeout(service.serviceContext, 30*time.Minute)
	defer cancelTraining()
	trainingJob.FineTunedParametersPath, err = service.trainModel(trainingContext, trainingJob)

	// 5. 训练成功标记为 pending_validation（待验证）；失败记录原因，取消或超时标记为 canceled。
	trainingJob.Status = "pending_validation"
	if err != nil {
		trainingJob.Status, trainingJob.Error = "failed", err.Error()
	}
	if err != nil && trainingContext.Err() != nil {
		trainingJob.Status = "canceled"
	}

	// 6. 保存最终结果：保存过程不继承 Service 的取消信号，另设十秒超时，避免记录停在 running。
	// 最后合并返回训练错误和保存错误，保留两边的失败原因。
	saveContext, cancelSave := context.WithTimeout(context.WithoutCancel(service.serviceContext), 10*time.Second)
	defer cancelSave()
	return errors.Join(err, service.localModelDAO.SaveTrainingJobResult(saveContext, trainingJob))
}

// trainModel 导出任务创建前的样本，执行 MLX；成功返回候选参数目录，失败返回空地址。
func (service *Service) trainModel(ctx context.Context, trainingJob *agentmodel.TrainingJob) (string, error) {
	// 训练等待设备令牌；开始后不受新用户任务影响，只有取消或超时才会停止。
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-service.deviceToken:
	}
	defer func() { service.deviceToken <- struct{}{} }()
	err := ctx.Err()
	if err != nil {
		return "", err
	}
	service.stopInferenceProcess()
	trainingMessages, err := service.localModelDAO.GetTrainingMessages(ctx, trainingJob.CreatedAt)
	if err != nil {
		return "", err
	}
	trainingDirectory := filepath.Join(service.config.DataDirectory, "jobs", trainingJob.ID)
	err = writeTrainingDatasets(trainingDirectory, trainingMessages)
	if err != nil {
		return "", err
	}
	// 新参数单独保存；不覆盖原模型或当前使用的微调参数，也不自动启用。
	fineTunedParametersPath := filepath.Join(trainingDirectory, "adapter")
	trainingLogPath := filepath.Join(trainingDirectory, "train.log")
	trainingLogFile, err := os.OpenFile(trainingLogPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", err
	}
	defer trainingLogFile.Close()
	trainingArguments := []string{
		"-u", "-m", "mlx_lm", "lora", "--model", service.config.BaseModelParametersPath,
		"--train", "--test", "--data", trainingDirectory, "--adapter-path", fineTunedParametersPath,
		"--batch-size", "1", "--num-layers", "4", "--iters", fmt.Sprint(service.config.TrainingIterations),
		"--max-seq-length", "1024", "--mask-prompt", "--test-batches", "-1",
	}
	// 已配置微调参数时，从这份参数继续训练。
	if service.config.FineTunedParametersPath != "" {
		trainingArguments = append(trainingArguments, "--resume-adapter-file", filepath.Join(service.config.FineTunedParametersPath, "adapters.safetensors"))
	}
	trainingCommand := exec.CommandContext(ctx, service.config.PythonExecutablePath, trainingArguments...)
	trainingCommand.Stdout, trainingCommand.Stderr = trainingLogFile, trainingLogFile
	// 子进程继承设备锁，防止 Worker 崩溃后另一进程抢占仍在训练的设备。
	trainingCommand.ExtraFiles = []*os.File{service.deviceLockFile}
	err = trainingCommand.Run()
	if err != nil {
		return "", fmt.Errorf("MLX training: %w (see %s)", err, trainingLogPath)
	}
	parametersFileInfo, err := os.Stat(filepath.Join(fineTunedParametersPath, "adapters.safetensors"))
	if err != nil {
		return "", err
	}
	if parametersFileInfo.Size() == 0 {
		return "", errors.New("MLX produced an empty adapter")
	}
	return fineTunedParametersPath, nil
}

// writeTrainingDatasets 按首问哈希分组：0～7 训练、8 验证、9 测试，同一首问始终归入同一集合。
func writeTrainingDatasets(trainingDirectory string, trainingMessages [][]*agentmodel.Message) error {
	if len(trainingMessages) < 10 {
		return errors.New("at least 10 confirmed examples are required")
	}
	err := os.MkdirAll(trainingDirectory, 0700)
	if err != nil {
		return err
	}
	datasetLines := make(map[string][]string)
	for _, messages := range trainingMessages {
		// 入库时已保证 user/assistant 交替，前面最多有一条 system 消息。
		firstUserMessage := messages[0]
		if firstUserMessage.Role == "system" {
			firstUserMessage = messages[1]
		}
		questionHash := sha256.Sum256([]byte(firstUserMessage.Content))
		datasetName := "train"
		if questionHash[len(questionHash)-1]%10 == 8 {
			datasetName = "valid"
		} else if questionHash[len(questionHash)-1]%10 == 9 {
			datasetName = "test"
		}
		exampleJSON, err := json.Marshal(map[string]any{"messages": messages})
		if err != nil {
			return err
		}
		datasetLines[datasetName] = append(datasetLines[datasetName], string(exampleJSON))
	}
	if len(datasetLines) != 3 {
		return errors.New("confirmed examples must populate train, valid and test partitions; add more examples")
	}
	for datasetName, jsonLines := range datasetLines {
		fileContent := strings.Join(jsonLines, "\n") + "\n"
		err := os.WriteFile(filepath.Join(trainingDirectory, datasetName+".jsonl"), []byte(fileContent), 0600)
		if err != nil {
			return err
		}
	}
	return nil
}

func isConnectedToACPower(ctx context.Context) bool {
	powerStatusOutput, err := exec.CommandContext(ctx, "/usr/bin/pmset", "-g", "batt").Output()
	return err == nil && strings.Contains(string(powerStatusOutput), "AC Power")
}
