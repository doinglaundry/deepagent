package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/deep"

	"eino-cli/backend/agent/middlewares"
	"eino-cli/backend/config"
	memorystore "eino-cli/backend/memory/store"
)

// MakeLeadAgent assembles the deep agent for `rt.AgentName` and returns
// the lead's *Trace alongside it. The trace pointer is what
// DeepAgentRuntime uses to reset the turn counter on /clear; subagent
// callers ignore it (their Trace dies with each ExecuteStream).
//
// Returns nil for *Trace if (somehow) the chain didn't include one
// — callers must treat it as optional. Today the chain always
// includes one, but pinning that into the signature would couple this
// function to middleware_chain.go's exact composition.
func MakeLeadAgent(
	ctx context.Context,
	rt RuntimeContext,
	cfg config.Config,
) (adk.ResumableAgent, *middlewares.Trace, error) {
	agentConfig, err := GetAgentConfig(cfg, rt.AgentName)
	if err != nil {
		return nil, nil, fmt.Errorf("load agent profile %q: %w", rt.AgentName, err)
	}
	modelCfg := cfg.Models[rt.ModelName]

	chatModel, err := buildChatModel(ctx, *modelCfg, rt.ThinkingEnabled, rt.ReasoningEffort)
	if err != nil {
		return nil, nil, err
	}

	backend := newLocalBackend("")
	shell := newLocalShell("")
	mem := NewMemoryAccessor(memorystore.NewStore(cfg.MemoryDir))

	prompt := ApplyPromptTemplate(PromptOptions{
		SubagentEnabled:        rt.SubagentEnabled,
		MaxConcurrentSubagents: rt.MaxConcurrentSubagents,
		AgentName:              rt.AgentName,
		AvailableSkills:        skillsFromProfile(agentConfig),
		Config:                 cfg,
		Mem:                    mem,
	})

	withGeneral := generalSubagentEnabled(ctx, rt)

	handlers := GetChatModelMiddlewares(ctx, cfg, mem, rt)

	deepCfg := &deep.Config{
		Name:                   rt.AgentName,
		Description:            "Deep Agent",
		ChatModel:              chatModel,
		Instruction:            prompt,
		MaxIteration:           defaultIterationLimit(agentConfig),
		WithoutGeneralSubAgent: !withGeneral,
		WithoutWriteTodos:      false,
		Middlewares:            GetAgentMiddleWares(rt),
		Handlers:               handlers,
	}

	applyToolGroups(deepCfg, agentConfig, backend, shell)

	agentImpl, err := deep.New(ctx, deepCfg)
	if err != nil {
		return nil, nil, fmt.Errorf("build deep agent: %w", err)
	}
	return agentImpl, middlewares.FindTrace(handlers), nil
}

func fallback(value, def string) string {
	if strings.TrimSpace(value) == "" {
		return def
	}
	return value
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
