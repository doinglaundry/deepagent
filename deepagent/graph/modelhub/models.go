// Package modelhub constructs the model clients used by the Core graph.
package modelhub

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

type Config struct {
	Name            string `yaml:"name"`
	Provider        string `yaml:"provider"`
	Model           string `yaml:"model"`
	BaseURL         string `yaml:"base_url"`
	APIKey          string `yaml:"api_key"`
	TimeoutSeconds  int    `yaml:"timeout_seconds"`
	MaxTokens       int    `yaml:"max_tokens"`
	ReasoningEffort string `yaml:"reasoning_effort"`
}

func New(ctx context.Context, config Config) (model.ToolCallingChatModel, error) {
	config.Name = strings.TrimSpace(config.Name)
	config.Model = strings.TrimSpace(config.Model)
	config.BaseURL = strings.TrimSpace(config.BaseURL)
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.ReasoningEffort = strings.ToLower(strings.TrimSpace(config.ReasoningEffort))
	if config.Name == "" || config.Model == "" {
		return nil, fmt.Errorf("model name and model identifier required")
	}
	if config.TimeoutSeconds < 0 || config.MaxTokens < 0 {
		return nil, fmt.Errorf("model budgets must not be negative")
	}
	timeout := time.Duration(config.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	provider := strings.ToLower(strings.TrimSpace(config.Provider))
	switch provider {
	case "openai", "openai-compatible", "kimi", "moonshot", "ark":
		if config.BaseURL == "" {
			switch provider {
			case "kimi", "moonshot":
				config.BaseURL = "https://api.moonshot.cn/v1"
			case "ark":
				// Ark's Chat API exposes the OpenAI-compatible tool/stream protocol.
				config.BaseURL = "https://ark.cn-beijing.volces.com/api/v3"
			}
		}
		modelConfig := &openai.ChatModelConfig{APIKey: config.APIKey, Model: config.Model, BaseURL: config.BaseURL, Timeout: timeout, ReasoningEffort: openai.ReasoningEffortLevel(config.ReasoningEffort)}
		if config.MaxTokens > 0 {
			modelConfig.MaxTokens = &config.MaxTokens
		}
		return openai.NewChatModel(ctx, modelConfig)

	default:
		return nil, fmt.Errorf("unsupported model provider %q", config.Provider)
	}
}
