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
)

// MakeLeadAgent assembles the deep agent for rt.AgentName and returns the
// lead's *Trace alongside it. The trace pointer is what DeepAgentRuntime
// uses to reset the turn counter on /clear; the *Trace may be nil.
func MakeLeadAgent(
	ctx context.Context,
	agentName string,
	IsPlanMode bool,
	IsSubagentEnabled bool,
	cfg *config.Config,
) (adk.ResumableAgent, *middlewares.Trace, error) {

	agentConfig := cfg.Agents[agentName]

	modelName := agentConfig.Model

	modelConfig := cfg.Models[modelName]

	chatModel, err := buildChatModel(ctx, modelConfig)
	if err != nil {
		return nil, nil, err
	}

	chatModel = wrapErrorHandling(chatModel, cfg.ErrorHandling)

	handlers := GetChatModelMiddlewares(ctx, agentName, IsSubagentEnabled, cfg, chatModel)

	deepCfg := &deep.Config{
		Name:                   agentName,
		Description:            "Deep Agent",
		ChatModel:              chatModel,
		Instruction:            GetSystemPrompt(agentName, IsSubagentEnabled, cfg),
		MaxIteration:           defaultIterationLimit(agentConfig),
		WithoutGeneralSubAgent: !IsSubagentEnabled,
		WithoutWriteTodos:      false,
		Middlewares:            GetAgentMiddleWares(IsPlanMode),
		Handlers:               handlers,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: tools.BuildBuiltinTools(cfg.RootDir),
			},
		},
	}

	agentImpl, err := deep.New(ctx, deepCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build deep agent: %w", err)
	}
	return agentImpl, middlewares.FindTrace(handlers), nil
}

func skillsFromProfile(p *config.AgentConfig) *AvailableSkills {
	if p == nil || p.Skills == nil {
		return AllSkills()
	}
	return SkillSet(p.Skills...)
}

func defaultIterationLimit(p *config.AgentConfig) int {
	const runtimeMaxIterDefault = 6
	if p == nil || p.MaxIteration <= 0 {
		return runtimeMaxIterDefault
	}
	return p.MaxIteration
}
