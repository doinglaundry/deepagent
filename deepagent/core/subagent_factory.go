package deepagents

import (
	"context"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/middleware/baseprompt"
	"eino-cli/deepagent/core/middleware/contextmanager"
	"eino-cli/deepagent/core/middleware/filesystem"
	"eino-cli/deepagent/core/middleware/subagent"
	"eino-cli/deepagent/core/middleware/web"
	deeptools "eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// createSubAgentFactory 创建子代理工厂函数
// 子代理不包含 SubAgentMiddleware，避免无限递归
func createSubAgentFactory(parentConfig *Config) subagent.SubAgentFactory {
	return func(ctx context.Context, chatModel model.ToolCallingChatModel, sa *subagent.SubAgent, defaultTools []tool.BaseTool, defaultMiddleware []middleware.Middleware) (subagent.SubAgentRunner, error) {
		opts := []Option{
			WithModel(chatModel),
			WithMaxSteps(sa.MaxSteps),
			withSubAgentFeatures(parentConfig),
			WithContextManager(contextmanager.New()),
		}
		// SP 归 BasePromptMiddleware
		if sa.SystemPrompt != "" {
			opts = append(opts, WithMiddleware(baseprompt.New(sa.SystemPrompt)))
		}
		for _, mw := range defaultMiddleware {
			if mw != nil {
				opts = append(opts, WithMiddleware(mw))
			}
		}
		parentFilesystemCfg := parentConfig.filesystemConfig()
		parentWorkDir := parentFilesystemCfg.WorkDir
		// 显式能力声明：文件系统（含本地上下文）
		if sa.EnableFilesystem && parentConfig.Backend != nil {
			opts = append(opts, WithMiddleware(filesystem.New(&filesystem.FilesystemConfig{
				Backend:           parentConfig.Backend,
				ReadOnly:          sa.ReadOnly,
				WorkDir:           parentWorkDir,
				DisableApplyPatch: parentFilesystemCfg.DisableApplyPatch,
				ToolMask:          sa.ToolMask,
			})))
		}
		// 显式能力声明：Web
		if sa.EnableWeb && parentConfig.WebConfig != nil {
			webConfig := *parentConfig.WebConfig
			webConfig.ToolMask = deeptools.CombineMasks(webConfig.ToolMask, sa.ToolMask)
			opts = append(opts, WithMiddleware(web.New(&webConfig)))
		}
		// 手动指定的 Tools
		if len(sa.Tools) > 0 {
			opts = append(opts, WithTools(sa.Tools...))
		}
		if sa.ToolMask != nil {
			opts = append(opts, WithToolMask(sa.ToolMask))
		}
		opts = append(opts, func(config *Config) { config.Depth = parentConfig.Depth + 1 })
		subAgent, err := New(ctx, opts...)
		if err != nil {
			return nil, err
		}
		registerSubAgentSharedCustomState(subAgent, parentConfig)
		return &subAgentRunnerAdapter{agent: subAgent}, nil
	}
}

func registerSubAgentSharedCustomState(subAgent *DeepAgent, parentConfig *Config) {
	if subAgent == nil || parentConfig == nil || subAgent.GraphState() == nil {
		return
	}
	for _, name := range parentConfig.SubAgentSharedCustomStateNames {
		stateful := parentConfig.CustomGraphState[name]
		if stateful != nil {
			subAgent.GraphState().RegisterRuntimeOnlyStateful(name, stateful)
		}
	}
}

// withSubAgentFeatures 为子代理继承父配置的部分功能
func withSubAgentFeatures(parentConfig *Config) (option Option) {
	option = func(c *Config) {
		if parentConfig.Backend != nil {
			c.Backend = parentConfig.Backend
		}
		// 继承 CheckpointStore（子代理含上下文管理器时需要）
		if parentConfig.CheckpointStore != nil {
			c.CheckpointStore = parentConfig.CheckpointStore
		}
		// 子代理不继承 SubAgents 和 HITL，避免复杂性和递归。
	}
	return option
}

// subAgentRunnerAdapter 将 DeepAgent 适配为 SubAgentRunner 接口
type subAgentRunnerAdapter struct{ agent *DeepAgent }

func (a *subAgentRunnerAdapter) Run(ctx context.Context, messages []*schema.Message, opts ...subagent.SubAgentRunOption) (*schema.Message, error) {
	var agentOpts []RunOptionFunc
	runOpt := subagent.ApplySubAgentRunOptions(opts...)
	if runOpt.CheckpointID != "" {
		agentOpts = append(agentOpts, WithCheckpointID(runOpt.CheckpointID))
	}
	return a.agent.Run(ctx, messages, agentOpts...)
}
func (a *subAgentRunnerAdapter) Stream(ctx context.Context, messages []*schema.Message, opts ...subagent.SubAgentRunOption) (*schema.StreamReader[*schema.Message], error) {
	var agentOpts []RunOptionFunc
	runOpt := subagent.ApplySubAgentRunOptions(opts...)
	if runOpt.CheckpointID != "" {
		agentOpts = append(agentOpts, WithCheckpointID(runOpt.CheckpointID))
	}
	return a.agent.Stream(ctx, messages, agentOpts...)
}
func (a *subAgentRunnerAdapter) Close(ctx context.Context) error { return a.agent.Close(ctx) }
func (a *subAgentRunnerAdapter) Depth() int                      { return a.agent.depth }
