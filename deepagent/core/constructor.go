package deepagents

import (
	"context"
	"errors"
	"fmt"

	"eino-cli/deepagent/core/backends"
	"eino-cli/deepagent/core/constant"
	"eino-cli/deepagent/core/hook"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/middleware/filesystem"
	"eino-cli/deepagent/core/middleware/patchtoolcalls"
	"eino-cli/deepagent/core/middleware/skill"
	"eino-cli/deepagent/core/middleware/subagent"
	"eino-cli/deepagent/core/middleware/web"
	"eino-cli/deepagent/core/tools"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
)

// ToolNodePreHandler 在非流式 tools 节点执行前处理完整 assistant message。
// 当前仅在 EnableStreamToolCall=false 时生效。
type ToolNodePreHandler func(ctx context.Context, input *schema.Message) (*schema.Message, error)

// ToolNodePostHandler 在非流式 tools 节点执行后处理完整 tool message 数组。
// 当前仅在 EnableStreamToolCall=false 时生效。
type ToolNodePostHandler func(ctx context.Context, output []*schema.Message) ([]*schema.Message, error)

// New 组装中间件、工具和状态，并编译可执行的 DeepAgent 图。
//
// 参数:
//   - ctx: 工具和状态初始化上下文。
//   - opts: 模型、后端、工具及执行限制等选项。
//
// 返回值:
//   - agent: 已编译但尚未执行的 Agent。
//   - err: 配置、后端、工具、状态或图构建失败。
//
// 功能特点:
//  1. 构造和执行分开；这里不会发起模型对话。
//
// 执行流程:
//  1. 合并配置并校验后端、子 Agent。
//  2. 构建中间件并收集工具。
//  3. 初始化状态、编译 Graph，保存到 DeepAgent。
//
// 使用示例:
//   - Thread 构建 Run 时调用 New(ctx, WithConfig(agentConfig))。
func New(ctx context.Context, opts ...Option) (agent *DeepAgent, err error) {
	config := buildCreateConfig(opts...)
	err = validateCreateConfig(config)
	if err != nil {
		return nil, err
	}
	if config.MaxSteps == 0 {
		config.MaxSteps = constant.DefaultMaxSteps
	}

	backend := selectBackend(config)
	err = validateBackendConfig(config, backend)
	if err != nil {
		return nil, err
	}
	loadSubAgentsFromDirs(ctx, config, backend)
	err = validateSubAgentNames(config)
	if err != nil {
		return nil, err
	}

	middlewares := buildCreateMiddlewares(config, backend)
	middlewares = applyMaxModelCalls(config, middlewares)

	chain := middleware.NewMiddlewareChain(middlewares...)
	allTools, err := collectAllTools(ctx, chain, config)
	if err != nil {
		return nil, err
	}

	agentState, err := buildAgentState(ctx, chain, config.CustomGraphState, config.CheckpointStore)
	if err != nil {
		return nil, err
	}

	runnable, err := buildGraphWithConfig(ctx, *config, allTools, chain)
	if err != nil {
		return nil, err
	}

	agent = &DeepAgent{
		runnable:        runnable,
		middlewareChain: chain,
		backend:         backend,
		callbacks:       append([]callbacks.Handler(nil), config.Callbacks...),
		hooks:           append(hook.HooksChain(nil), config.Hooks...),
		graphState:      agentState,
		depth:           config.Depth,
	}
	return agent, nil
}

func buildCreateConfig(opts ...Option) (config *Config) {
	config = &Config{
		MaxSteps: constant.DefaultMaxSteps,
	}
	for _, opt := range opts {
		opt(config)
	}
	return config
}

func validateCreateConfig(config *Config) (err error) {
	if config.Model == nil {
		return errors.New(constant.ErrMsgModelRequired)
	}
	if config.MaxModelCalls < 0 {
		return errors.New("max model calls must be >= 0")
	}
	for _, name := range config.SubAgentSharedCustomStateNames {
		if config.CustomGraphState == nil || config.CustomGraphState[name] == nil {
			return fmt.Errorf("sub-agent shared custom state %q is not configured", name)
		}
	}
	return nil
}

func validateBackendConfig(config *Config, backend backends.Backend) (err error) {
	if backend != nil {
		return nil
	}
	if config.FilesystemConfig != nil {
		return errors.New("filesystem requires backend or workdir")
	}
	if len(config.SubAgentsDirs) > 0 {
		return errors.New("subagent dirs require backend or workdir")
	}
	for _, sa := range config.SubAgents {
		if sa != nil && sa.EnableFilesystem {
			return errors.New("subagent filesystem requires backend or workdir")
		}
	}
	return nil
}

func selectBackend(config *Config) (backend backends.Backend) {
	if config.Backend != nil {
		return config.Backend
	}
	if workDir := config.filesystemWorkDir(); workDir != "" {
		return backends.NewFilesystemBackend(&backends.FilesystemBackendConfig{
			RootDir:     workDir,
			VirtualMode: true,
		})
	}
	return nil
}

func loadSubAgentsFromDirs(ctx context.Context, config *Config, backend backends.Backend) {
	for _, dir := range config.SubAgentsDirs {
		loaded, err := subagent.LoadSubAgentsFromDir(ctx, dir, backend, nil)
		if err == nil {
			config.SubAgents = append(config.SubAgents, loaded...)
		}
	}
}

func buildCreateMiddlewares(config *Config, backend backends.Backend) (middlewares []middleware.Middleware) {
	var subAgentSkillMiddlewareFactory func() middleware.Middleware

	if config.ContextManager != nil {
		middlewares = append(middlewares, config.ContextManager)
	}

	if config.EnablePatchToolCalls {
		middlewares = append(middlewares, patchtoolcalls.New())
	}

	if config.SkillLoader != nil {
		middlewares = append(middlewares, skill.NewWithConfig(config.SkillLoader, &skill.MiddlewareConfig{
			ToolMask: config.ToolMask,
		}))
		skillLoader := config.SkillLoader
		subAgentSkillMiddlewareFactory = func() middleware.Middleware {
			return skill.New(skillLoader)
		}
	}

	if config.FilesystemConfig != nil {
		filesystemCfg := config.filesystemConfig()
		middlewares = append(middlewares, filesystem.New(&filesystem.FilesystemConfig{
			Backend:               backend,
			WorkDir:               filesystemCfg.WorkDir,
			ReadOnly:              filesystemCfg.ReadOnly,
			DisableUploadDownload: filesystemCfg.DisableUploadDownload,
			DisableExecute:        filesystemCfg.DisableExecute,
			DisableApplyPatch:     filesystemCfg.DisableApplyPatch,
			CommandTimeout:        filesystemCfg.CommandTimeout,
			ToolMask:              config.ToolMask,
		}))
	}

	if !config.DisableSubAgent && len(config.SubAgents) > 0 {
		middlewares = append(middlewares, subagent.New(&subagent.SubAgentConfig{
			SubAgents:                      config.SubAgents,
			DefaultModel:                   config.Model,
			DefaultTools:                   config.Tools,
			Factory:                        createSubAgentFactory(config),
			SubAgentSkillMiddlewareFactory: subAgentSkillMiddlewareFactory,
			ContextInjector:                config.SubAgentContextInjector,
			ToolMask:                       config.ToolMask,
			EnableTaskStreaming:            config.EnableSubAgentTaskStreaming,
		}))
	}

	if config.WebConfig != nil {
		webConfig := *config.WebConfig
		webConfig.ToolMask = tools.CombineMasks(webConfig.ToolMask, config.ToolMask)
		middlewares = append(middlewares, web.New(&webConfig))
	}

	for _, mw := range config.Middlewares {
		if mw != nil {
			middlewares = append(middlewares, mw)
		}
	}

	return middlewares
}
