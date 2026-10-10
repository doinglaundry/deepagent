// Package localmodel 管理当前 Mac 上的 MLX 推理和微调进程。
package localmodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	daldb "eino-cli/deepagent/dal/db"
	"eino-cli/deepagent/graph/modelhub"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var ErrModelBusy = errors.New("local model device is busy")

type Config struct {
	ModelName               string `yaml:"name"`                       // 样本和训练作业的隔离标识。
	BaseModelParametersPath string `yaml:"base_model_parameters_path"` // 原模型参数目录，包含配置、分词器和权重。
	FineTunedParametersPath string `yaml:"fine_tuned_parameters_path"` // 微调参数目录；为空时只加载原模型。
	DataDirectory           string `yaml:"data_directory"`             // 保存日志、训练数据和新生成的候选参数。
	PythonExecutablePath    string `yaml:"python"`                     // MLX 所用的 Python 可执行文件路径。
	InferencePort           int    `yaml:"port"`                       // 本地推理服务的监听端口。
	TrainingIterations      int    `yaml:"iterations"`                 // 每次微调执行的训练步数。
	EnableAutoTraining      bool   `yaml:"auto_train"`                 // 是否在满足空闲条件时自动训练。
}

// Service 持有设备锁和子进程，协调推理、训练与取消。
type Service struct {
	config                Config
	localModelDAO         *daldb.LocalModelDAO
	localChatModel        model.ToolCallingChatModel
	parametersFingerprint string // 原模型与微调参数的内容指纹，用于中断恢复校验。
	serviceContext        context.Context
	cancelService         context.CancelFunc
	trainingLoopDone      chan struct{}
	deviceToken           chan struct{} // 唯一设备令牌：取走即占用，归还后才能继续训练或推理。
	deviceLockFile        *os.File      // 跨进程文件锁，防止多个 Worker 同时使用设备。
	inferenceProcess      *exec.Cmd
	inferenceProcessDone  chan struct{}
	threadActivityMutex   sync.Mutex
	activeThreadCount     int
	idleStartedAt         time.Time
}

// New 准备目录、模型客户端和设备锁；首次推理时才启动 MLX 加载参数。
func New(ctx context.Context, config Config, localModelDAO *daldb.LocalModelDAO) (*Service, error) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return nil, errors.New("MLX personal model requires Apple Silicon macOS")
	}
	if strings.TrimSpace(config.ModelName) == "" || strings.TrimSpace(config.BaseModelParametersPath) == "" || strings.TrimSpace(config.DataDirectory) == "" {
		return nil, errors.New("local_model.name, base_model_parameters_path and data_directory are required")
	}
	if config.PythonExecutablePath == "" {
		config.PythonExecutablePath = "python3"
	}
	if config.InferencePort == 0 {
		config.InferencePort = 18080
	}
	if config.TrainingIterations == 0 {
		config.TrainingIterations = 100
	}
	if config.InferencePort < 1 || config.InferencePort > 65535 || config.TrainingIterations < 1 {
		return nil, errors.New("invalid local model port or iterations")
	}
	// 运行数据目录可写，必须与加载参数的目录分开。
	dataDirectory, err := filepath.Abs(config.DataDirectory)
	if err != nil {
		return nil, err
	}
	err = os.MkdirAll(dataDirectory, 0700)
	if err != nil {
		return nil, err
	}
	config.DataDirectory, err = filepath.EvalSymlinks(dataDirectory)
	if err != nil {
		return nil, err
	}
	// 读取两类参数文件，计算内容指纹，供 checkpoint 恢复时核对。
	parametersHasher := sha256.New()

	// 原模型参数目录必填，先把原模型文件计入指纹。
	config.BaseModelParametersPath, err = validateAndHashParametersDirectory(config.BaseModelParametersPath, config.DataDirectory, parametersHasher)
	if err != nil {
		return nil, fmt.Errorf("base model parameters directory: %w", err)
	}

	// 微调参数目录可选，配置后再计入同一个指纹。
	if config.FineTunedParametersPath != "" {
		config.FineTunedParametersPath, err = validateAndHashParametersDirectory(config.FineTunedParametersPath, config.DataDirectory, parametersHasher)
		if err != nil {
			return nil, fmt.Errorf("fine-tuned parameters directory: %w", err)
		}
	}
	localChatModel, err := modelhub.New(ctx, modelhub.Config{Name: "local-personal-model", Provider: "openai-compatible", Model: config.BaseModelParametersPath, BaseURL: fmt.Sprintf("http://127.0.0.1:%d/v1", config.InferencePort), APIKey: "local", TimeoutSeconds: 120, MaxTokens: 512})
	if err != nil {
		return nil, err
	}
	deviceLockFile, err := os.OpenFile(filepath.Join(os.TempDir(), fmt.Sprintf("deepagent-mlx-%d.lock", os.Getuid())), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	err = syscall.Flock(int(deviceLockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		deviceLockFile.Close()
		return nil, errors.New("another MLX process owns this Mac device; stop it before starting this worker")
	}
	if localModelDAO != nil {
		err = localModelDAO.FailAbandonedTrainingJobs(ctx)
		if err != nil {
			deviceLockFile.Close()
			return nil, err
		}
	}
	serviceContext, cancelService := context.WithCancel(ctx)
	service := &Service{
		config: config, localModelDAO: localModelDAO, localChatModel: localChatModel, parametersFingerprint: hex.EncodeToString(parametersHasher.Sum(nil)),
		serviceContext: serviceContext, cancelService: cancelService, deviceLockFile: deviceLockFile, idleStartedAt: time.Now(),
		trainingLoopDone: make(chan struct{}), deviceToken: make(chan struct{}, 1),
	}
	service.deviceToken <- struct{}{}
	go service.runTrainingLoop()
	return service, nil
}

// validateAndHashParametersDirectory 校验参数目录，并按文件名顺序把相对路径和内容写入指纹。
func validateAndHashParametersDirectory(parametersDirectory, dataDirectory string, parametersHasher io.Writer) (string, error) {
	parametersDirectoryInfo, err := os.Stat(parametersDirectory)
	if err != nil {
		return "", err
	}
	if !parametersDirectoryInfo.IsDir() {
		return "", errors.New("parameter paths must be local directories")
	}
	parametersDirectory, err = filepath.EvalSymlinks(parametersDirectory)
	if err != nil {
		return "", err
	}
	parametersDirectory, err = filepath.Abs(parametersDirectory)
	if err != nil {
		return "", err
	}
	relativePath, err := filepath.Rel(parametersDirectory, dataDirectory)
	if err != nil {
		return "", err
	}
	if relativePath == "." || relativePath != ".." && !strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) {
		return "", errors.New("data directory must be outside base and fine-tuned parameter directories")
	}
	err = filepath.WalkDir(parametersDirectory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != parametersDirectory && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		relativePath, _ := filepath.Rel(parametersDirectory, path)
		_, _ = io.WriteString(parametersHasher, relativePath+"\x00")
		file, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		_, copyErr := io.Copy(parametersHasher, file)
		return errors.Join(copyErr, file.Close())
	})
	if err != nil {
		return "", fmt.Errorf("local model snapshot: %w", err)
	}
	return parametersDirectory, nil
}

func (service *Service) GetParametersFingerprint() string { return service.parametersFingerprint }

// BeginThread 只记录用户任务的起止，供后续训练判断空闲时间；不会中断已开始的训练。
func (service *Service) BeginThread() func() {
	service.threadActivityMutex.Lock()
	service.activeThreadCount++
	service.threadActivityMutex.Unlock()
	return func() {
		service.threadActivityMutex.Lock()
		service.activeThreadCount--
		service.idleStartedAt = time.Now()
		service.threadActivityMutex.Unlock()
	}
}

func (service *Service) Generate(ctx context.Context, prompt string) (string, error) {
	ctx, cancelInference := context.WithTimeout(ctx, 5*time.Minute)
	defer cancelInference()
	// Service 关闭后，异步执行 cancelInference，取消本次推理。
	stopCancelCallback := context.AfterFunc(service.serviceContext, cancelInference)
	// 推理结束时阻止尚未开始的回调；已开始的回调不受影响。
	defer stopCancelCallback()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-service.deviceToken:
	default:
		// 设备正忙时立即返回，让 API 模型继续处理当前请求。
		return "", ErrModelBusy
	}
	defer func() { service.deviceToken <- struct{}{} }()
	err := ctx.Err()
	if err != nil {
		return "", err
	}
	err = service.startInferenceProcess(ctx)
	if err != nil {
		return "", err
	}
	response, err := service.localChatModel.Generate(ctx, []*schema.Message{schema.UserMessage(prompt)})
	if err != nil {
		service.stopInferenceProcess()
		return "", err
	}
	if response == nil || len(response.ToolCalls) != 0 || strings.TrimSpace(response.Content) == "" {
		return "", errors.New("local model returned no text answer")
	}
	return response.Content, nil
}

// startInferenceProcess 启动或复用 MLX 推理进程，等待服务就绪后返回。
// 调用方须持有 deviceToken，保证推理和训练互斥。
func (service *Service) startInferenceProcess(ctx context.Context) error {
	// 1. 复用仍在运行的进程；已退出的进程先清理，再重新启动。
	if service.inferenceProcess != nil {
		select {
		case <-service.inferenceProcessDone:
			service.stopInferenceProcess()
		default:
			return nil
		}
	}

	// 2. 临时绑定端口，检查此刻是否被占用；检查后关闭，交给 MLX 监听。
	inferenceAddress := "127.0.0.1:" + strconv.Itoa(service.config.InferencePort)
	listener, err := net.Listen("tcp", inferenceAddress)
	if err != nil {
		return err
	}
	listener.Close()

	// 3. 追加保存推理进程的正常输出和错误输出，启动失败时可查看 server.log。
	inferenceLogFile, err := os.OpenFile(filepath.Join(service.config.DataDirectory, "server.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer inferenceLogFile.Close()

	// 4. 拼接 python -m mlx_lm server 命令，加载原模型及可选的微调参数。
	inferenceArguments := []string{"-m", "mlx_lm", "server", "--model", service.config.BaseModelParametersPath, "--host", "127.0.0.1", "--port", strconv.Itoa(service.config.InferencePort), "--chat-template-args", `{"enable_thinking":false}`}
	if service.config.FineTunedParametersPath != "" {
		inferenceArguments = append(inferenceArguments, "--adapter-path", service.config.FineTunedParametersPath)
	}
	// 进程跟随 Service 的生命周期，可供多次推理复用。
	inferenceCommand := exec.CommandContext(service.serviceContext, service.config.PythonExecutablePath, inferenceArguments...)
	inferenceCommand.Stdout, inferenceCommand.Stderr = inferenceLogFile, inferenceLogFile
	// 子进程继承设备锁；即使 Worker 崩溃，只要模型进程还活着，锁就不会释放。
	inferenceCommand.ExtraFiles = []*os.File{service.deviceLockFile}

	// 5. Start 只负责启动进程，服务是否就绪要等后面的健康检查确认。
	err = inferenceCommand.Start()
	if err != nil {
		return err
	}
	service.inferenceProcess, service.inferenceProcessDone = inferenceCommand, make(chan struct{})
	// 固定本次进程的退出信号；后台等它退出后关闭通道，避免重启时误关新通道。
	inferenceProcessDone := service.inferenceProcessDone
	go func() { _ = inferenceCommand.Wait(); close(inferenceProcessDone) }()

	// 6. 每 100 毫秒触发健康检查，单次请求最多等待 1 秒；返回 200 才视为就绪。
	healthCheckClient := &http.Client{Timeout: time.Second}
	healthCheckTicker := time.NewTicker(100 * time.Millisecond)
	defer healthCheckTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			// 等待期间请求取消或超时，清理正在启动的进程。
			service.stopInferenceProcess()
			return ctx.Err()
		case <-service.inferenceProcessDone:
			// 服务尚未就绪，进程就退出了，返回启动失败。
			service.stopInferenceProcess()
			return errors.New("MLX server exited; check local model server.log")
		case <-healthCheckTicker.C:
			healthCheckRequest, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+inferenceAddress+"/health", nil)
			response, requestErr := healthCheckClient.Do(healthCheckRequest)
			if requestErr == nil {
				response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
	}
}

// 持有设备使用权时调用；等待进程退出后，再释放模型资源。
func (service *Service) stopInferenceProcess() {
	if service.inferenceProcess == nil {
		return
	}
	_ = service.inferenceProcess.Process.Kill()
	<-service.inferenceProcessDone
	service.inferenceProcess, service.inferenceProcessDone = nil, nil
}

func (service *Service) Close() error {
	service.cancelService()
	<-service.trainingLoopDone
	<-service.deviceToken
	defer func() { service.deviceToken <- struct{}{} }()
	service.stopInferenceProcess()
	return service.deviceLockFile.Close()
}
