// Package modelhub constructs the model clients used by the Core graph.
package modelhub

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	claude "github.com/cloudwego/eino-ext/components/model/claude"
	openai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
)

type Config struct {
	Name                 string `yaml:"name"`
	Provider             string `yaml:"provider"`
	Model                string `yaml:"model"`
	BaseURL              string `yaml:"base_url"`
	APIKey               string `yaml:"api_key"`
	TimeoutSeconds       int    `yaml:"timeout_seconds"`
	MaxTokens            int    `yaml:"max_tokens"`
	SupportsThinking     bool   `yaml:"supports_thinking"`
	ThinkingBudgetTokens int    `yaml:"thinking_budget_tokens"`
	ReasoningEffort      string `yaml:"reasoning_effort"`
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
	if c.TimeoutSeconds < 0 || c.MaxTokens < 0 || c.ThinkingBudgetTokens < 0 {
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
	case "claude", "anthropic":
		cfg := &claude.Config{Model: c.Model, APIKey: c.APIKey, MaxTokens: c.MaxTokens, HTTPClient: &http.Client{Timeout: timeout}}
		if cfg.MaxTokens == 0 {
			cfg.MaxTokens = 8192
		}
		if c.BaseURL != "" {
			cfg.BaseURL = &c.BaseURL
		}
		if c.SupportsThinking {
			budget := c.ThinkingBudgetTokens
			if budget == 0 {
				budget = 4096
			}
			if cfg.MaxTokens <= budget {
				cfg.MaxTokens = budget + 1024
			}
			cfg.Thinking = &claude.Thinking{Enable: true, BudgetTokens: budget}
		}
		return claude.NewChatModel(ctx, cfg)
	default:
		return nil, fmt.Errorf("unsupported model provider %q", c.Provider)
	}
}
