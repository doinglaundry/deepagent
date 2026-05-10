package agent

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	claudemodel "github.com/cloudwego/eino-ext/components/model/claude"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"

	"eino-cli/backend/config"
)

// buildSummaryChatModel returns the chat model used by the summarization
// middleware (no thinking, no reasoning_effort). nil on any failure — a
// misconfigured summary model must never block the lead agent.
func buildSummaryChatModel(
	ctx context.Context,
	cfg *config.Config,
) model.BaseChatModel {
	summaryModelName := strings.TrimSpace(cfg.Summarization.ModelName)
	if summaryModelName == "" {
		summaryModelName = cfg.DefaultModel
	}
	summaryModelCfg := cfg.Models[summaryModelName]
	summaryModel, err := buildChatModel(ctx, summaryModelCfg)
	if err != nil {
		return nil
	}
	return summaryModel
}

// buildChatModel is the agent-package chat model factory. thinkingEnabled is
// only honoured by Claude; reasoningEffort is only honoured by OpenAI.
func buildChatModel(
	ctx context.Context,
	modelConfig *config.ModelConfig,
) (model.BaseChatModel, error) {
	provider := strings.ToLower(strings.TrimSpace(modelConfig.Provider))
	apiKey := strings.TrimSpace(modelConfig.APIKey)
	timeout := time.Duration(modelConfig.TimeoutSeconds) * time.Second

	switch provider {
	case "claude", "anthropic":
		return buildClaudeChatModel(ctx, modelConfig, apiKey, timeout, modelConfig.SupportsThinking)
	case "openai":
		return openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{
			APIKey:          apiKey,
			Model:           strings.TrimSpace(modelConfig.Model),
			BaseURL:         strings.TrimSpace(modelConfig.BaseURL),
			Timeout:         timeout,
			ReasoningEffort: parseReasoningEffort(modelConfig.ReasoningEffort),
		})
	case "kimi", "moonshot":
		return buildKimiChatModel(ctx, modelConfig, apiKey, timeout)
	default:
		return nil, fmt.Errorf("unsupported model provider %q", modelConfig.Provider)
	}
}

func buildClaudeChatModel(
	ctx context.Context,
	cfg *config.ModelConfig,
	apiKey string,
	timeout time.Duration,
	thinkingEnabled bool,
) (model.BaseChatModel, error) {
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
	if thinkingEnabled {
		budget := cfg.ThinkingBudgetTokens
		if budget <= 0 {
			budget = 4096
		}
		// Claude requires MaxTokens > BudgetTokens; bump if too small.
		if claudeCfg.MaxTokens <= budget {
			claudeCfg.MaxTokens = budget + 1024
		}
		claudeCfg.Thinking = &claudemodel.Thinking{
			Enable:       true,
			BudgetTokens: budget,
		}
	}
	return claudemodel.NewChatModel(ctx, claudeCfg)
}

func buildKimiChatModel(
	ctx context.Context,
	cfg *config.ModelConfig,
	apiKey string,
	timeout time.Duration,
) (model.BaseChatModel, error) {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = "https://api.moonshot.cn/v1"
	}
	modelName := strings.TrimSpace(cfg.Model)
	if !strings.HasPrefix(strings.ToLower(modelName), "moonshot") {
		modelName = "moonshot-v1-8k"
	}
	return openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{
		APIKey:  apiKey,
		Model:   modelName,
		BaseURL: baseURL,
		Timeout: timeout,
	})
}

// parseReasoningEffort maps the textual effort knob to the OpenAI typed enum;
// empty / unknown returns "" (no override).
func parseReasoningEffort(s string) openaimodel.ReasoningEffortLevel {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low":
		return openaimodel.ReasoningEffortLevelLow
	case "medium":
		return openaimodel.ReasoningEffortLevelMedium
	case "high":
		return openaimodel.ReasoningEffortLevelHigh
	default:
		return ""
	}
}
