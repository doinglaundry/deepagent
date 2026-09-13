package deepagents

import (
	"context"
	"maps"
	"time"

	"eino-cli/deepagent/core/backends"
	"eino-cli/deepagent/core/hook"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/middleware/plan"
	"eino-cli/deepagent/core/middleware/skill"
	"eino-cli/deepagent/core/middleware/subagent"
	"eino-cli/deepagent/core/middleware/web"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
)

type HITLConfig struct {
	ToolPolicyGates map[string]tools.ToolPolicyGate

	NeedReviewAndEditTools map[string]tools.NeedReviewAndEdit
	NeedFollowUpTool       bool
}

type ContinueAfterModelFunc func(context.Context) (bool, error)

type FilesystemConfig struct {
	WorkDir               string
	ReadOnly              bool
	DisableUploadDownload bool
	DisableExecute        bool
	DisableApplyPatch     bool
	CommandTimeout        time.Duration
}

// 它描述的是默认装配意图，而不是 DeepAgent 的核心构造模型。
type Config struct {
	// Hooks are run in order around agent and model lifecycle boundaries.
	Hooks hook.HooksChain
	// Model 聊天模型（必需）
	Model model.ToolCallingChatModel

	// MaxSteps 最大执行步数
	MaxSteps int

	// MaxModelCalls limits ChatModel invocations in one logical DeepAgent run.
	// Leave it <= 0 to disable this budget. Unlike MaxSteps, this counts model
	// calls and is persisted in GraphState across HITL resume when checkpointing
	// is enabled.
	MaxModelCalls int

	// Tools 用户自定义工具
	Tools []tool.BaseTool

	// ToolMask 控制最终对 model 可见且可由服务端执行的工具集合。
	// 返回 true 表示保留该工具，返回 false 表示将其从最终工具集合中过滤掉。
	ToolMask tools.Mask

	// SubAgents 子代理配置
	SubAgents []*subagent.SubAgent

	// SubAgentsDirs 子代理配置目录（从 SUBAGENT.yaml 加载）
	SubAgentsDirs []string

	// SubAgentContextInjector 用于给子 agent 注入上下文
	SubAgentContextInjector subagent.SubAgentContextInjector

	// EnableSubAgentTaskStreaming 使 SubAgentMiddleware 的 task 工具流式输出子 agent 最终回复。
	EnableSubAgentTaskStreaming bool

	// SkillLoader 技能加载器
	SkillLoader skill.Loader

	// FilesystemConfig 非空时启用文件系统能力
	FilesystemConfig *FilesystemConfig

	// EnablePatchToolCalls 是否启用工具调用修补
	EnablePatchToolCalls bool

	// EnableStreamToolCall 是否启用流式工具调用执行
	EnableStreamToolCall bool

	// DisableSubAgent 是否禁用子代理中间件
	// 默认情况下，当 backend != nil 或有 SubAgents 配置时自动启用
	// 设置为 true 可强制禁用子代理能力
	DisableSubAgent bool

	// WebConfig 非空时启用 Web 工具
	WebConfig *web.WebConfig

	HITLConfig *HITLConfig

	ContextManager middleware.Middleware

	// Backend 文件系统后端
	// 如果传入的是 SandboxBackend，则自动启用 execute 工具
	Backend backends.Backend

	// Middlewares 自定义中间件
	Middlewares []middleware.Middleware

	// Callbacks 注入到 Eino compose 运行时的 callback handlers。
	Callbacks []callbacks.Handler

	// CheckpointStore eino 原生的 checkpoint 存储
	// 用于实现对话恢复、状态回滚等功能
	CheckpointStore compose.CheckPointStore

	// InterruptBeforeNodes 在这些节点执行前中断
	// 用于人工审批等场景
	InterruptBeforeNodes []string

	// InterruptAfterNodes 在这些节点执行后中断
	InterruptAfterNodes []string

	// Stateful
	CustomGraphState map[string]types.RunTimeStateful

	// SubAgentSharedCustomStateNames 指定哪些 CustomGraphState 可被子 agent 共享。
	// 共享状态由父 agent checkpoint 持久化，子 agent 仅运行时读写，不保存到自身 checkpoint。
	SubAgentSharedCustomStateNames []string

	Depth int

	// ToolInfoRewriter 用于重写工具的 ToolInfo（name/desc）
	// 如果设置，所有工具的 Info 将经过此函数处理
	ToolInfoRewriter tools.ToolInfoRewriter

	// ToolNodePreHandler 在非流式 tools 节点执行前处理完整 assistant message。
	// 当前仅在 EnableStreamToolCall=false 时生效。
	ToolNodePreHandler ToolNodePreHandler

	// ToolNodePostHandler 在非流式 tools 节点执行后处理完整 tool message 数组。
	// 当前仅在 EnableStreamToolCall=false 时生效。
	ToolNodePostHandler ToolNodePostHandler

	// ContinueAfterModel is checked when the model has no tool call and would
	// otherwise finish. Returning true starts another model pass with empty input.
	ContinueAfterModel ContinueAfterModelFunc
}

// Clone returns an independent configuration container. Runtime dependencies
// and function values remain shared, while mutable maps, slices, and nested
// configuration values are copied.
func (c *Config) Clone() (cloned *Config) {
	if c == nil {
		return &Config{}
	}
	value := *c
	cloned = &value
	cloned.Tools = append([]tool.BaseTool(nil), c.Tools...)
	cloned.SubAgents = append([]*subagent.SubAgent(nil), c.SubAgents...)
	cloned.SubAgentsDirs = append([]string(nil), c.SubAgentsDirs...)
	cloned.Middlewares = append([]middleware.Middleware(nil), c.Middlewares...)
	cloned.Callbacks = append([]callbacks.Handler(nil), c.Callbacks...)
	cloned.InterruptBeforeNodes = append([]string(nil), c.InterruptBeforeNodes...)
	cloned.InterruptAfterNodes = append([]string(nil), c.InterruptAfterNodes...)
	cloned.CustomGraphState = maps.Clone(c.CustomGraphState)
	cloned.SubAgentSharedCustomStateNames = append([]string(nil), c.SubAgentSharedCustomStateNames...)
	cloned.Hooks = append(hook.HooksChain(nil), c.Hooks...)

	if c.FilesystemConfig != nil {
		filesystem := *c.FilesystemConfig
		cloned.FilesystemConfig = &filesystem
	}
	if c.WebConfig != nil {
		webConfig := *c.WebConfig
		cloned.WebConfig = &webConfig
	}
	if c.HITLConfig != nil {
		hitl := *c.HITLConfig
		hitl.ToolPolicyGates = maps.Clone(c.HITLConfig.ToolPolicyGates)
		hitl.NeedReviewAndEditTools = maps.Clone(c.HITLConfig.NeedReviewAndEditTools)
		cloned.HITLConfig = &hitl
	}
	return cloned
}

func (c *Config) filesystemConfig() (filesystem FilesystemConfig) {
	if c != nil && c.FilesystemConfig != nil {
		return *c.FilesystemConfig
	}
	return filesystem
}

func (c *Config) filesystemWorkDir() (workDir string) {
	return c.filesystemConfig().WorkDir
}

// Option 是 New builder 的配置选项函数。
type Option func(*Config)

// WithConfig uses one complete Config as the starting point for New. Options
// that follow it may still override individual fields.
func WithConfig(source *Config) (option Option) {
	option = func(target *Config) {
		configured := source.Clone()
		*target = *configured
	}
	return option
}

func WithCustomGraphState(fields map[string]types.RunTimeStateful) Option {
	return func(c *Config) {
		c.CustomGraphState = fields
	}
}

// WithSubAgentSharedCustomState 指定子 agent 可共享的 CustomGraphState key。
// 共享状态仍由父 agent checkpoint 持久化；子 agent 不会把这些 state 写入自身 checkpoint。
func WithSubAgentSharedCustomState(names ...string) Option {
	return func(c *Config) {
		c.SubAgentSharedCustomStateNames = append(c.SubAgentSharedCustomStateNames, names...)
	}
}

// WithModel 设置模型
func WithModel(m model.ToolCallingChatModel) Option {
	return func(c *Config) {
		c.Model = m
	}
}

// WithMaxSteps 设置最大执行步数
func WithMaxSteps(steps int) Option {
	return func(c *Config) {
		c.MaxSteps = steps
	}
}

// WithMaxModelCalls 设置单个逻辑 run 内最多允许的模型调用次数。
//
// 该限制不同于 WithMaxSteps：MaxSteps 是 Eino graph run step 保护；
// MaxModelCalls 只在每次 ChatModel 调用前扣减，并会随 GraphState 保存，
// 因此在 HITL resume 后继续使用剩余额度。
func WithMaxModelCalls(calls int) Option {
	return func(c *Config) {
		c.MaxModelCalls = calls
	}
}

// WithWorkDir 设置工作目录
func WithWorkDir(dir string) Option {
	return func(c *Config) {
		if c.FilesystemConfig == nil {
			c.FilesystemConfig = &FilesystemConfig{}
		}
		c.FilesystemConfig.WorkDir = dir
	}
}

// WithTools 设置用户自定义工具
func WithTools(tools ...tool.BaseTool) Option {
	return func(c *Config) {
		c.Tools = append(c.Tools, tools...)
	}
}

// WithToolMask 设置最终工具集合过滤器。
// 返回 true 表示保留该工具，返回 false 表示将其从最终工具集合中过滤掉。
func WithToolMask(mask tools.Mask) Option {
	return func(c *Config) {
		c.ToolMask = mask
	}
}

func WithContinueAfterModel(continueAfterModel ContinueAfterModelFunc) (option Option) {
	option = func(c *Config) {
		c.ContinueAfterModel = continueAfterModel
	}
	return option
}

// WithSubAgents 设置子代理
func WithSubAgents(agents ...*subagent.SubAgent) Option {
	return func(c *Config) {
		c.SubAgents = append(c.SubAgents, agents...)
	}
}

// WithSubAgentsDir 从目录加载子代理配置
// 目录结构应为: dir/agent-name/SUBAGENT.yaml
func WithSubAgentsDir(dir string) Option {
	return WithSubAgentsDirs(dir)
}

// WithSubAgentsDirs 设置多个子代理配置目录
func WithSubAgentsDirs(dirs ...string) Option {
	return func(c *Config) {
		c.SubAgentsDirs = append(c.SubAgentsDirs, dirs...)
	}
}

func WithSubAgentContextInjector(i subagent.SubAgentContextInjector) Option {
	return func(c *Config) {
		c.SubAgentContextInjector = i
	}
}

// WithSubAgentTaskStreaming 使 task 工具实现 StreamableTool。
// 当前仅流式输出子 agent 最终 assistant response，不转发子 agent 内部事件。
func WithSubAgentTaskStreaming() Option {
	return func(c *Config) {
		c.EnableSubAgentTaskStreaming = true
	}
}

// WithSkillLoader 设置技能加载器
func WithSkillLoader(loader skill.Loader) Option {
	return func(c *Config) {
		c.SkillLoader = loader
	}
}

// WithPlanMiddleware enables the lightweight update_plan progress checklist.
func WithPlanMiddleware(cfg *plan.PlanMiddlewareConfig) Option {
	return func(c *Config) {
		c.Middlewares = append(c.Middlewares, plan.New(cfg))
	}
}

// WithFilesystem 启用文件系统访问
func WithFilesystem() (option Option) {
	option = func(c *Config) {
		if c.FilesystemConfig == nil {
			c.FilesystemConfig = &FilesystemConfig{}
		}
	}
	return option
}

// WithFilesystemConfig 使用自定义配置启用文件系统访问。
func WithFilesystemConfig(cfg *FilesystemConfig) (option Option) {
	option = func(c *Config) {
		if cfg == nil {
			c.FilesystemConfig = &FilesystemConfig{}
			return
		}
		cloned := *cfg
		c.FilesystemConfig = &cloned
	}
	return option
}

// WithDisableSubAgent 禁用子代理中间件
func WithDisableSubAgent() Option {
	return func(c *Config) {
		c.DisableSubAgent = true
	}
}

// WithDisableUploadDownload 禁用 upload_files 和 download_files 工具
func WithDisableUploadDownload() Option {
	return func(c *Config) {
		if c.FilesystemConfig == nil {
			c.FilesystemConfig = &FilesystemConfig{}
		}
		c.FilesystemConfig.DisableUploadDownload = true
	}
}

// WithDisableExecute 禁用 execute 工具
// 即使 Backend 实现了 SandboxBackend 接口，也不暴露 execute 工具
func WithDisableExecute() Option {
	return func(c *Config) {
		if c.FilesystemConfig == nil {
			c.FilesystemConfig = &FilesystemConfig{}
		}
		c.FilesystemConfig.DisableExecute = true
	}
}

// WithBackend 设置文件系统后端
func WithBackend(b backends.Backend) Option {
	return func(c *Config) {
		c.Backend = b
	}
}

// WithSandboxBackend 设置沙箱后端（支持命令执行）
// SandboxBackend 实现了 Backend 接口，所以会同时启用文件操作和命令执行
func WithSandboxBackend(b backends.SandboxBackend) Option {
	return WithBackend(b)
}

// WithPatchToolCalls 启用工具调用修补
func WithPatchToolCalls() Option {
	return func(c *Config) {
		c.EnablePatchToolCalls = true
	}
}

// WithStreamToolCall 启用流式工具调用执行
func WithStreamToolCall() Option {
	return func(c *Config) {
		c.EnableStreamToolCall = true
	}
}

// WithWeb 启用 Web 工具（web_search, http_request, fetch_url）
func WithWeb() (option Option) {
	option = func(c *Config) {
		c.WebConfig = web.DefaultConfig()
	}
	return option
}

// WithWebConfig 使用自定义配置启用 Web 工具
func WithWebConfig(config *web.WebConfig) (option Option) {
	option = func(c *Config) {
		if config == nil {
			c.WebConfig = web.DefaultConfig()
			return
		}
		cloned := *config
		c.WebConfig = &cloned
	}
	return option
}

// WithMiddleware 添加自定义中间件
func WithMiddleware(m middleware.Middleware) Option {
	return func(c *Config) {
		c.Middlewares = append(c.Middlewares, m)
	}
}

// WithHooks adds lifecycle hooks to the agent configuration. Hooks are copied
// into the constructed agent and executed in declaration order.
func WithHooks(hooks ...hook.Hooks) Option {
	return func(c *Config) {
		c.Hooks = append(c.Hooks, hooks...)
	}
}

// WithDefaultCallbacks 注入每次 Run/Stream 默认携带的 Eino compose callback handlers。
func WithDefaultCallbacks(handlers ...callbacks.Handler) Option {
	return func(c *Config) {
		for _, handler := range handlers {
			if handler != nil {
				c.Callbacks = append(c.Callbacks, handler)
			}
		}
	}
}

// WithCheckpointStore 设置 checkpoint 存储（eino 原生接口）
// 用于实现对话恢复、状态回滚等功能
func WithCheckpointStore(store compose.CheckPointStore) Option {
	return func(c *Config) {
		c.CheckpointStore = store
	}
}

// WithInterruptBeforeNodes 设置在指定节点执行前中断
// 用于人工审批等场景
func WithInterruptBeforeNodes(nodes ...string) Option {
	return func(c *Config) {
		c.InterruptBeforeNodes = append(c.InterruptBeforeNodes, nodes...)
	}
}

// WithInterruptAfterNodes 设置在指定节点执行后中断
func WithInterruptAfterNodes(nodes ...string) Option {
	return func(c *Config) {
		c.InterruptAfterNodes = append(c.InterruptAfterNodes, nodes...)
	}
}

// WithAllFeatures 启用所有功能
func WithAllFeatures() (option Option) {
	option = func(c *Config) {
		if c.FilesystemConfig == nil {
			c.FilesystemConfig = &FilesystemConfig{}
		}
		c.EnablePatchToolCalls = true
		c.WebConfig = web.DefaultConfig()
	}
	return option
}

func WithHITLConfig(cfg *HITLConfig) Option {
	return func(c *Config) {
		c.HITLConfig = cfg
	}
}

func WithContextManager(manager middleware.Middleware) Option {
	return func(c *Config) {
		c.ContextManager = manager
	}
}

// WithToolInfoRewriter 设置工具信息重写器
// rewriter 接收原始 ToolInfo，返回修改后的 ToolInfo
func WithToolInfoRewriter(rewriter tools.ToolInfoRewriter) Option {
	return func(c *Config) {
		c.ToolInfoRewriter = rewriter
	}
}

// WithToolNodePreHandler 设置非流式 tools 节点的 pre handler。
func WithToolNodePreHandler(handler ToolNodePreHandler) Option {
	return func(c *Config) {
		c.ToolNodePreHandler = handler
	}
}

// WithToolNodePostHandler 设置非流式 tools 节点的 post handler。
func WithToolNodePostHandler(handler ToolNodePostHandler) Option {
	return func(c *Config) {
		c.ToolNodePostHandler = handler
	}
}
