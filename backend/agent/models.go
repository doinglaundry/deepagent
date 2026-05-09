package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	claudemodel "github.com/cloudwego/eino-ext/components/model/claude"
	openaimodel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"

	"eino-cli/backend/config"
)

// getThinkingEnabled honours requested but downgrades to false (with a warn) when
// the resolved model does not declare SupportsThinking.
func getThinkingEnabled(requested bool, modelCfg *config.ModelConfig, modelName string) bool {
	if !requested {
		return false
	}
	if modelCfg != nil && modelCfg.SupportsThinking {
		return true
	}
	slog.Warn("thinking enabled but model does not support it; downgrading",
		"model", modelName)
	return false
}

// buildSummaryChatModel returns the chat model used by the summarization
// middleware (no thinking, no reasoning_effort). nil on any failure — a
// misconfigured summary model must never block the lead agent.
func buildSummaryChatModel(
	ctx context.Context,
	cfg config.Config,
) model.BaseChatModel {
	summaryModelName := strings.TrimSpace(cfg.Summarization.ModelName)
	if summaryModelName == "" {
		summaryModelName = cfg.DefaultModel
	}
	summaryModelCfg := cfg.Models[summaryModelName]
	summaryModel, err := buildChatModel(ctx, *summaryModelCfg, false, "")
	if err != nil {
		return nil
	}
	return summaryModel
}

// buildChatModel is the agent-package chat model factory. thinkingEnabled is
// only honoured by Claude; reasoningEffort is only honoured by OpenAI.
func buildChatModel(
	ctx context.Context,
	cfg config.ModelConfig,
	thinkingEnabled bool,
	reasoningEffort string,
) (model.BaseChatModel, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	apiKey := strings.TrimSpace(cfg.APIKey)
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second

	switch provider {
	case "claude", "anthropic":
		return buildClaudeChatModel(ctx, cfg, apiKey, timeout, thinkingEnabled)
	case "openai":
		return openaimodel.NewChatModel(ctx, &openaimodel.ChatModelConfig{
			APIKey:          apiKey,
			Model:           strings.TrimSpace(cfg.Model),
			BaseURL:         strings.TrimSpace(cfg.BaseURL),
			Timeout:         timeout,
			ReasoningEffort: parseReasoningEffort(reasoningEffort),
		})
	case "kimi", "moonshot":
		return buildKimiChatModel(ctx, cfg, apiKey, timeout)
	default:
		return nil, fmt.Errorf("unsupported model provider %q", cfg.Provider)
	}
}

func buildClaudeChatModel(
	ctx context.Context,
	cfg config.ModelConfig,
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
	cfg config.ModelConfig,
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
