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

func New(ctx context.Context, c Config) (model.ToolCallingChatModel, error) {
	c.Name = strings.TrimSpace(c.Name)
	c.Model = strings.TrimSpace(c.Model)
	c.BaseURL = strings.TrimSpace(c.BaseURL)
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.ReasoningEffort = strings.ToLower(strings.TrimSpace(c.ReasoningEffort))
	if c.Name == "" || c.Model == "" {
		return nil, fmt.Errorf("model name and model identifier required")
	}
	if c.TimeoutSeconds < 0 || c.MaxTokens < 0 {
		return nil, fmt.Errorf("model budgets must not be negative")
	}
	timeout := time.Duration(c.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	provider := strings.ToLower(strings.TrimSpace(c.Provider))
	switch provider {
	case "openai", "openai-compatible", "kimi", "moonshot", "ark":
		if c.BaseURL == "" {
			switch provider {
			case "kimi", "moonshot":
				c.BaseURL = "https://api.moonshot.cn/v1"
			case "ark":
				// Ark's Chat API exposes the OpenAI-compatible tool/stream protocol.
				c.BaseURL = "https://ark.cn-beijing.volces.com/api/v3"
			}
		}
		cfg := &openai.ChatModelConfig{APIKey: c.APIKey, Model: c.Model, BaseURL: c.BaseURL, Timeout: timeout, ReasoningEffort: openai.ReasoningEffortLevel(c.ReasoningEffort)}
		if c.MaxTokens > 0 {
			cfg.MaxTokens = &c.MaxTokens
		}
		return openai.NewChatModel(ctx, cfg)

	default:
		return nil, fmt.Errorf("unsupported model provider %q", c.Provider)
	}
}
