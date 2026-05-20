package middlewares

import (
	"context"
	"eino-cli/backend/agent/memory"
	"eino-cli/backend/config"
	"eino-cli/backend/consts"
	"fmt"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/summarization"
	"github.com/cloudwego/eino/components/model"
)

func NewSummarization(
	ctx context.Context,
	memCfg config.Memory,
	updater *memory.MemoryUpdater,
	summaryModel model.BaseChatModel,
) (adk.ChatModelAgentMiddleware, error) {
	if summaryModel == nil {
		return nil, fmt.Errorf("summarization enabled but no chat model provided")
	}

	condition := &summarization.TriggerCondition{
		ContextTokens:   190000,
		ContextMessages: 200,
	}

	mw, err := summarization.New(ctx, &summarization.Config{
		Model:   summaryModel,
		Trigger: condition,
		Callback: func(ctx context.Context, before, _ adk.ChatModelAgentState) error {
			if updater == nil {
				return nil
			}
			flushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			return updater.Run(flushCtx, summaryModel, consts.DefaultAgentKey, before.Messages, true)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("build summarization middleware: %w", err)
	}
	return mw, nil
}
