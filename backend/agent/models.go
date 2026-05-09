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

// getThinkingEnabled honours rt.ThinkingEnabled but downgrades to
// false (with a warn log) when the resolved model declares it doesn't
// support extended thinking. Mirrors deerflow's silent-downgrade with
// an explicit log signal.
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

// buildSummaryChatModel returns the chat model the summarization
// middleware should use. When cfg.Summarization.ModelName names a
// model different from the lead agent's, build it on the side so
// summarization runs against a cheaper / shorter-context client.
// Summarization never wants thinking nor reasoning_effort — both add
// latency for no quality gain on a compaction task — so we pass
// false / "" explicitly. Any failure (missing config, build error)
// falls back to fallbackModel with a warn log; a misconfigured
// summary model must never block the lead agent.
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

// buildChatModel is the agent-package chat model factory.
//
// Mirrors deerflow's create_chat_model(name, thinking_enabled,
// reasoning_effort): the lead-agent assembly resolves both flags from
// the RuntimeContext and hands them in here so the actual API client
// is constructed with the right knobs.
//
// thinkingEnabled is honoured by Claude (extended-thinking; budget
// comes from cfg.ThinkingBudgetTokens or a 4096 default).
// reasoningEffort is honoured by OpenAI (low/medium/high →
// openai.ReasoningEffortLevel). Kimi / Moonshot ignore both — neither
// is in the upstream API surface.
func buildChatModel(
	ctx context.Context,
	cfg config.ModelConfig,
	thinkingEnabled bool,
	reasoningEffort string,
) (model.BaseChatModel, error) {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	// cfg.APIKey arrives already resolved to a literal credential:
	// the YAML loader expands $ENV / api_key_env at decode time, and
	// normalizeConfig falls back to the provider's canonical env via
	// defaultAPIKeyEnv when neither was supplied. Empty here means
	// "no credential anywhere" — the chat model factory below will
	// reject it.
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

// parseReasoningEffort maps the textual effort knob coming from
// RuntimeContext / RunnableConfig onto the typed enum the OpenAI
// client expects. An empty / unknown value falls through as the zero
// value (== "no override"), matching Python's behaviour where a
// missing reasoning_effort lets the upstream default apply.
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
