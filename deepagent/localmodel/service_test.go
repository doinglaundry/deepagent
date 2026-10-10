package localmodel

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestServiceProcessCancellationAndOwnership(t *testing.T) {
	encodedConfig := os.Getenv("DEEPAGENT_TEST_MLX_WORKER_EXIT")
	if encodedConfig != "" {
		var config Config
		err := json.Unmarshal([]byte(encodedConfig), &config)
		if err != nil {
			t.Fatal(err)
		}
		service, err := New(context.Background(), config, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, err = service.Generate(context.Background(), "hello")
		if err != nil {
			t.Fatal(err)
		}
		err = os.WriteFile(filepath.Join(config.DataDirectory, "orphan.pid"), []byte(strconv.Itoa(service.inferenceProcess.Process.Pid)), 0600)
		if err != nil {
			t.Fatal(err)
		}
		os.Exit(23) // Simulate a worker exit that bypasses all cleanup.
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Mac MLX service")
	}
	pythonExecutablePath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python is required for HTTP process fixture")
	}
	directory := t.TempDir()
	inferenceServerScriptPath := filepath.Join(directory, "server.py")
	err = os.WriteFile(inferenceServerScriptPath, []byte(`import sys,json,time
from http.server import HTTPServer,BaseHTTPRequestHandler
port=int(sys.argv[sys.argv.index('--port')+1])
class Handler(BaseHTTPRequestHandler):
 def do_GET(self):
  self.send_response(200);self.end_headers()
 def do_POST(self):
  data=json.loads(self.rfile.read(int(self.headers['Content-Length'])))
  text=data['messages'][0]['content']
  if text=='wait':time.sleep(10)
  payload=json.dumps({'id':'local','object':'chat.completion','choices':[{'index':0,'message':{'role':'assistant','content':'local answer'},'finish_reason':'stop'}]})
  self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(payload.encode())
 def log_message(self,*args):pass
HTTPServer(('127.0.0.1',port),Handler).serve_forever()
`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	pythonWrapperPath := filepath.Join(directory, "python")
	err = os.WriteFile(pythonWrapperPath, []byte(fmt.Sprintf("#!/bin/sh\nexec '%s' '%s' \"$@\"\n", pythonExecutablePath, inferenceServerScriptPath)), 0700)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	inferencePort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	baseModelParametersPath := filepath.Join(directory, ".model")
	err = os.Mkdir(baseModelParametersPath, 0700)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(baseModelParametersPath, "config.json"), []byte(`{}`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	// Resolve both paths before checking containment, including /tmp aliases on macOS.
	modelSymlinkPath := filepath.Join(directory, "model-alias")
	err = os.Symlink(baseModelParametersPath, modelSymlinkPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(context.Background(), Config{ModelName: "test-model", BaseModelParametersPath: baseModelParametersPath, DataDirectory: filepath.Join(modelSymlinkPath, "state")}, nil)
	if err == nil || !strings.Contains(err.Error(), "outside base and fine-tuned parameter directories") {
		t.Fatalf("nested state directory accepted through a symlink: %v", err)
	}
	config := Config{ModelName: "test-model", BaseModelParametersPath: baseModelParametersPath, DataDirectory: filepath.Join(directory, "state"), PythonExecutablePath: pythonWrapperPath, InferencePort: inferencePort}
	service, err := New(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	_, err = New(context.Background(), config, nil)
	if err == nil {
		t.Fatal("second process owner accepted")
	}
	answer, err := service.Generate(context.Background(), "hello")
	if err != nil || answer != "local answer" {
		t.Fatalf("answer=%q err=%v", answer, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err = service.Generate(ctx, "wait")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel=%v", err)
	}
	if service.inferenceProcess != nil {
		t.Fatal("canceled inference left a process behind")
	}
	answer, err = service.Generate(context.Background(), "hello again")
	if err != nil || answer != "local answer" {
		t.Fatalf("restart answer=%q err=%v", answer, err)
	}
	// New weights alter checkpoint identity, independent of path/name.
	parametersFingerprint := service.GetParametersFingerprint()
	err = os.WriteFile(filepath.Join(baseModelParametersPath, "config.json"), []byte(`{"changed":true}`), 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = service.Close()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	workerProcess := exec.Command(os.Args[0], "-test.run=^TestServiceProcessCancellationAndOwnership$")
	workerProcess.Env = append(os.Environ(), "DEEPAGENT_TEST_MLX_WORKER_EXIT="+string(encoded))
	childErr := workerProcess.Run()
	var exitErr *exec.ExitError
	if !errors.As(childErr, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("worker fixture: %v", childErr)
	}
	pidText, err := os.ReadFile(filepath.Join(config.DataDirectory, "orphan.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(string(pidText))
	if err != nil {
		t.Fatal(err)
	}
	orphanInferenceProcess, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = orphanInferenceProcess.Kill() })
	_, err = New(context.Background(), config, nil)
	if err == nil {
		t.Fatal("worker exit released GPU lock while model process was alive")
	}
	_ = orphanInferenceProcess.Kill()
	time.Sleep(100 * time.Millisecond)
	updatedService, err := New(context.Background(), config, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer updatedService.Close()
	if parametersFingerprint == updatedService.GetParametersFingerprint() || strings.TrimSpace(parametersFingerprint) == "" {
		t.Fatal("weights/configuration changes did not change version")
	}
}

func TestServiceModelAndAdapterFingerprint(t *testing.T) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Skip("Mac MLX service")
	}
	directory := t.TempDir()
	baseModelParametersPath := filepath.Join(directory, "model")
	fineTunedParametersPath := filepath.Join(directory, "adapter")
	err := os.Mkdir(baseModelParametersPath, 0700)
	if err != nil {
		t.Fatal(err)
	}
	err = os.Mkdir(fineTunedParametersPath, 0700)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(baseModelParametersPath, "config.json"), []byte("base"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(fineTunedParametersPath, "adapters.safetensors"), []byte("adapter"), 0600)
	if err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name                    string
		fineTunedParametersPath string
		parametersHashInput     string
	}{
		{"model only", "", "config.json\x00base"},
		{"model then adapter", fineTunedParametersPath, "config.json\x00baseadapters.safetensors\x00adapter"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			service, err := New(context.Background(), Config{
				ModelName: "test-model", BaseModelParametersPath: baseModelParametersPath, FineTunedParametersPath: testCase.fineTunedParametersPath,
				DataDirectory: filepath.Join(directory, "state"),
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer service.Close()
			expectedParametersHash := sha256.Sum256([]byte(testCase.parametersHashInput))
			if service.GetParametersFingerprint() != hex.EncodeToString(expectedParametersHash[:]) {
				t.Fatalf("model/adapter fingerprint changed: %s", service.GetParametersFingerprint())
			}
		})
	}
}

func TestGenerateDoesNotWaitForBusyDevice(t *testing.T) {
	// 没有可用令牌表示设备正被训练或另一次推理使用。
	service := &Service{serviceContext: context.Background(), deviceToken: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := service.Generate(ctx, "answer without waiting for training")
	if !errors.Is(err, ErrModelBusy) {
		t.Fatalf("busy local model waited instead of returning immediately: %v", err)
	}
}
