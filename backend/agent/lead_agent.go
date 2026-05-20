package agent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"
	"github.com/cloudwego/eino/compose"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/agent/tools"
	"eino-cli/backend/config"
	"eino-cli/backend/consts"
	"eino-cli/backend/sandbox"
)

func MakeLeadAgent(
	ctx context.Context,
	isSubagentEnabled bool,
	getPlanMode func() bool,
	cfg *config.Config,
) (adk.ResumableAgent, *middlewares.Trace, error) {

	agentName := consts.DefaultAgentKey
	modelConfig := cfg.Models[cfg.DefaultModel]

	chatModel, err := buildChatModel(ctx, modelConfig)
	if err != nil {
		return nil, nil, err
	}
	chatModel = wrapErrorHandling(chatModel)

	sandboxManager := sandbox.Default()
	handlers := GetChatModelMiddlewares(ctx, agentName, isSubagentEnabled, getPlanMode, cfg, chatModel, sandboxManager)

	deepCfg := &deep.Config{
		Name:                   agentName,
		Description:            "Deep Agent",
		ChatModel:              chatModel,
		Instruction:            GetSystemPrompt(agentName, isSubagentEnabled, cfg),
		MaxIteration:           consts.DefaultAgentIterations,
		WithoutGeneralSubAgent: !isSubagentEnabled,
		WithoutWriteTodos:      false,
		Handlers:               handlers,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: tools.BuildBuiltinTools(cfg, sandboxManager),
			},
		},
	}

	agentImpl, err := deep.New(ctx, deepCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build deep agent: %w", err)
	}
	return agentImpl, middlewares.FindTrace(handlers), nil
}
