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

func getModelConfig(cfg config.Config, modelName string) (config.ModelConfig, error) {
	name := strings.TrimSpace(modelName)
	if name == "" {
		name = strings.TrimSpace(cfg.DefaultModel)
	}
	if name == "" {
		return config.ModelConfig{}, fmt.Errorf("default model is required")
	}

	modelCfg, ok := cfg.Models[name]
	if !ok {
		return config.ModelConfig{}, fmt.Errorf("model %q not found", name)
	}
	if strings.TrimSpace(modelCfg.Name) == "" {
		modelCfg.Name = name
	}

	return *modelCfg, nil
}

func getAgentConfig(cfg config.Config, agentName string) (config.AgentConfig, error) {
	name := strings.TrimSpace(agentName)
	if name == "" {
		name = strings.TrimSpace(cfg.DefaultAgent)
	}
	if name == "" {
		return config.AgentConfig{}, fmt.Errorf("default agent is required")
	}

	agentCfg, ok := cfg.Agents[name]
	if !ok {
		return config.AgentConfig{}, fmt.Errorf("agent %q not found", name)
	}

	return agentCfg, nil
}

func BuildRuntime(ctx context.Context, cfg config.Config, store adk.CheckPointStore) (Runtime, error) {
	modelCfg, err := getModelConfig(cfg, cfg.DefaultModel)
	if err != nil {
		return nil, err
	}
	agentCfg, err := getAgentConfig(cfg, cfg.DefaultAgent)
	if err != nil {
		return nil, err
	}

	return CreateRuntimeFromModel(ctx, modelCfg, agentCfg, store)
}

func CreateRuntimeFromModel(ctx context.Context, modelCfg config.ModelConfig, agentCfg config.AgentConfig, store adk.CheckPointStore) (Runtime, error) {
	switch strings.ToLower(strings.TrimSpace(modelCfg.Provider)) {
	case "claude", "anthropic", "openai", "kimi", "moonshot":
		return NewDeepAgentRuntime(ctx, modelCfg, agentCfg, store)
	default:
		return nil, fmt.Errorf("unsupported model provider %q", modelCfg.Provider)
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
