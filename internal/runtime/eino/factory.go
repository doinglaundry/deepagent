package eino

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	claudemodel "github.com/cloudwego/eino-ext/components/model/claude"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"

	"eino-cli/internal/config"
)

func BuildRuntime(ctx context.Context, cfg config.Config, store adk.CheckPointStore) (Runtime, error) {
	modelName := strings.TrimSpace(cfg.DefaultModel)
	if modelName == "" {
		return nil, fmt.Errorf("default model is required")
	}
	mc, ok := cfg.Models[modelName]
	if !ok {
		return nil, fmt.Errorf("model %q not found", modelName)
	}
	if strings.TrimSpace(mc.Name) == "" {
		mc.Name = modelName
	}

	agentName := strings.TrimSpace(cfg.DefaultAgent)
	if agentName == "" {
		return nil, fmt.Errorf("default agent is required")
	}
	ac, ok := cfg.Agents[agentName]
	if !ok {
		return nil, fmt.Errorf("agent %q not found", agentName)
	}

	switch strings.ToLower(strings.TrimSpace(mc.Provider)) {
	case "claude", "anthropic", "openai", "kimi", "moonshot":
		return NewDeepAgentRuntime(ctx, *mc, ac, store)
	default:
		return nil, fmt.Errorf("unsupported model provider %q", mc.Provider)
	}
}

func buildBaseChatModel(ctx context.Context, cfg config.ModelConfig) (model.BaseChatModel, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	apiKey := strings.TrimSpace(os.Getenv(strings.TrimSpace(cfg.APIKeyEnv)))
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second

	switch provider {
	case "claude", "anthropic":
		claudeCfg := &claudemodel.Config{
			Model:     strings.TrimSpace(cfg.Model),
			MaxTokens: 2048,
			APIKey:    apiKey,
		}
		if timeout > 0 {
			claudeCfg.HTTPClient = &http.Client{Timeout: timeout}
		}
		if baseURL := strings.TrimSpace(cfg.BaseURL); baseURL != "" {
			claudeCfg.BaseURL = &baseURL
		}
		chatModel, err := claudemodel.NewChatModel(ctx, claudeCfg)
		if err != nil {
			return nil, fmt.Errorf("build claude chat model: %w", err)
		}
		return chatModel, nil
	case "openai":
		openaiCfg := &openaimodel.ChatModelConfig{
			APIKey:  apiKey,
			Model:   strings.TrimSpace(cfg.Model),
			BaseURL: strings.TrimSpace(cfg.BaseURL),
			Timeout: timeout,
		}
		chatModel, err := openaimodel.NewChatModel(ctx, openaiCfg)
		if err != nil {
			return nil, fmt.Errorf("build openai chat model: %w", err)
		}
		return chatModel, nil
	case "kimi", "moonshot":
		baseURL := strings.TrimSpace(cfg.BaseURL)
		if baseURL == "" {
			baseURL = "https://api.moonshot.cn/v1"
		}
		modelName := strings.TrimSpace(cfg.Model)
		if !strings.HasPrefix(strings.ToLower(modelName), "moonshot") {
			modelName = "moonshot-v1-8k"
		}
		kimiCfg := &openaimodel.ChatModelConfig{
			APIKey:  apiKey,
			Model:   modelName,
			BaseURL: baseURL,
			Timeout: timeout,
		}
		chatModel, err := openaimodel.NewChatModel(ctx, kimiCfg)
		if err != nil {
			return nil, fmt.Errorf("build kimi chat model: %w", err)
		}
		return chatModel, nil
	default:
		return nil, fmt.Errorf("unsupported model provider %q", cfg.Provider)
	}
}
