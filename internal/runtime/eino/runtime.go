package eino

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Runtime interface {
	Execute(ctx context.Context, prompt string) (Result, error)
	Name() string
}

type NoopRuntime struct {
	ModelName string
}

type LocalServiceRuntime struct {
	BaseURL string
	Model   string
	Client  *http.Client
}

type chatRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
}

type chatResponse struct {
	Output string `json:"output"`
}

func NewNoopRuntime(modelName string) NoopRuntime {
	return NoopRuntime{ModelName: strings.TrimSpace(modelName)}
}

func NewLocalServiceRuntime(baseURL, model string, timeout time.Duration) LocalServiceRuntime {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return LocalServiceRuntime{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Model:   strings.TrimSpace(model),
		Client:  &http.Client{Timeout: timeout},
	}
}

func (n NoopRuntime) Execute(context.Context, string) (Result, error) {
	return SuccessResult(fmt.Sprintf("stub response from %s", n.Name())), nil
}

func (n NoopRuntime) Name() string {
	if strings.TrimSpace(n.ModelName) == "" {
		return "noop-model"
	}
	return strings.TrimSpace(n.ModelName)
}

func (r LocalServiceRuntime) Execute(ctx context.Context, prompt string) (Result, error) {
	payload, err := json.Marshal(chatRequest{Model: r.Name(), Prompt: prompt})
	if err != nil {
		return Result{}, fmt.Errorf("marshal runtime request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.BaseURL+"/v1/chat", bytes.NewReader(payload))
	if err != nil {
		return Result{}, fmt.Errorf("build runtime request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.Client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("local runtime unavailable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return Result{}, fmt.Errorf("local runtime returned status %d", resp.StatusCode)
	}

	var body chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Result{}, fmt.Errorf("decode runtime response: %w", err)
	}
	if strings.TrimSpace(body.Output) == "" {
		return Result{}, fmt.Errorf("local runtime returned empty output")
	}

	return SuccessResult(body.Output), nil
}

func (r LocalServiceRuntime) Name() string {
	if strings.TrimSpace(r.Model) == "" {
		return "local-model"
	}
	return strings.TrimSpace(r.Model)
}
