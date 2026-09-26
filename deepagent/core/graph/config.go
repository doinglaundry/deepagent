package graph

import (
	"context"
	"eino-cli/deepagent/core/backend"
	"eino-cli/deepagent/core/hooks"
	"github.com/cloudwego/eino/callbacks"
	"maps"
	"time"

	"eino-cli/deepagent/core/internal/conversation"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"eino-cli/deepagent/core/types"
	"github.com/cloudwego/eino/components/model"
	einotool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type Conversation interface {
	ReloadHistory(context.Context) error
	AddHistory(context.Context, string, ...*schema.Message) error
	History(context.Context) []*schema.Message
	BuildRequest(context.Context, []*schema.Message) ([]*schema.Message, error)
	ContextUsage() conversation.ContextUsageSnapshot
	RecordModelUsage(context.Context, *model.TokenUsage)
	RunUsage() types.Usage
	RestoreRunUsage(context.Context, types.Usage) error
	CompactNeeded(context.Context) bool
	Compact(context.Context, string) (*conversation.ContextCompactedPayload, error)
}

type Config struct {
	Hooks                          hook.HooksChain
	SubAgents                      []*SubAgent
	SubAgentsDirs                  []string
	SubAgentContextInjector        SubAgentContextInjector
	EnableSubAgentTaskStreaming    bool
	SkillLoader                    backend.SkillLoader
	FilesystemConfig               *FilesystemConfig
	EnablePatchToolCalls           bool
	DisableSubAgent                bool
	WebConfig                      *tools.WebConfig
	HITLConfig                     *HITLConfig
	Workspace                      backend.ToolWorkspace
	Callbacks                      []callbacks.Handler
	InterruptBeforeNodes           []string
	InterruptAfterNodes            []string
	CustomGraphState               map[string]types.RunTimeStateful
	SubAgentSharedCustomStateNames []string
	ToolInfoRewriter               tools.ToolInfoRewriter
	ToolNodePreHandler             ToolNodePreHandler
	ToolNodePostHandler            ToolNodePostHandler
	Middlewares                    []middleware.Middleware
	EnableStreamToolCall           bool
	Model                          model.ToolCallingChatModel
	ToolDescriptors                []tools.Descriptor
	ToolMask                       tools.Mask
	ReadOnlyToolsOnly              bool
	Policy                         tools.Policy
	Parallelism                    int
	MaxSteps                       int
	MaxModelCalls                  int
	Name                           string
	Depth                          int
	ThreadID                       string
	RunID                          string
	Prompts                        []*schema.Message
	Conversation                   Conversation
	Emit                           func(context.Context, types.RuntimeEvent) error
	DrainInput                     func(context.Context, string) ([]types.Input, bool, error)
	CheckpointStore                compose.CheckPointStore
}
type Option func(*Config)

func WithConfig(source *Config) Option { return func(c *Config) { *c = *source.Clone() } }

func WithModel(m model.ToolCallingChatModel) Option { return func(c *Config) { c.Model = m } }

type RunOptions struct {
	streamDone          chan struct{}
	InputMeta           []any
	CheckpointID        string
	WriteToCheckpointID string
	ForceNewRun         bool
	ResumeInterruptIDs  []string
	ResumeData          map[string]any
	composeOpts         []compose.Option
	chunk               types.ModelChunkSink
}
type RunOptionFunc func(*RunOptions)

func WithCallbacks(handlers ...callbacks.Handler) RunOptionFunc {
	return func(o *RunOptions) { o.composeOpts = append(o.composeOpts, compose.WithCallbacks(handlers...)) }
}

type ResumeOptions struct {
	CheckpointID string
	InterruptIDs []string
	Data         map[string]any
}

func WithCheckpointID(id string) RunOptionFunc { return func(o *RunOptions) { o.CheckpointID = id } }
func WithWriteToCheckpointID(id string) RunOptionFunc {
	return func(o *RunOptions) { o.WriteToCheckpointID = id }
}
func WithForceNewRun() RunOptionFunc { return func(o *RunOptions) { o.ForceNewRun = true } }
func WithResume(ids ...string) RunOptionFunc {
	return func(o *RunOptions) { o.ResumeInterruptIDs = append(o.ResumeInterruptIDs, ids...) }
}
func WithResumeData(data map[string]any) RunOptionFunc {
	return func(o *RunOptions) {
		if o.ResumeData == nil {
			o.ResumeData = make(map[string]any)
		}
		for id, value := range data {
			o.ResumeData[id] = value
		}
	}
}

func WithInputMetadata(meta ...any) RunOptionFunc {
	return func(o *RunOptions) { o.InputMeta = append([]any(nil), meta...) }
}

type HITLConfig struct {
	ToolPolicyGates map[string]tools.ToolPolicyGate

	NeedFollowUpTool bool
}

type FilesystemConfig struct {
	WorkDir               string
	localFromOption       bool
	ReadOnly              bool
	DisableUploadDownload bool
	DisableExecute        bool
	DisableApplyPatch     bool
	CommandTimeout        time.Duration
}

type ToolNodePreHandler func(context.Context, *schema.Message) (*schema.Message, error)
type ToolNodePostHandler func(context.Context, []*schema.Message) ([]*schema.Message, error)

func (c *Config) Clone() (cloned *Config) {
	if c == nil {
		return &Config{}
	}
	value := *c
	cloned = &value
	cloned.ToolDescriptors = append([]tools.Descriptor(nil), c.ToolDescriptors...)
	cloned.Prompts = append([]*schema.Message(nil), c.Prompts...)
	cloned.SubAgents = append([]*SubAgent(nil), c.SubAgents...)
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

func WithCustomGraphState(fields map[string]types.RunTimeStateful) Option {
	return func(c *Config) {
		c.CustomGraphState = fields
	}
}

func WithSubAgentSharedCustomState(names ...string) Option {
	return func(c *Config) {
		c.SubAgentSharedCustomStateNames = append(c.SubAgentSharedCustomStateNames, names...)
	}
}

func WithMaxSteps(steps int) Option {
	return func(c *Config) {
		c.MaxSteps = steps
	}
}

func WithMaxModelCalls(calls int) Option {
	return func(c *Config) {
		c.MaxModelCalls = calls
	}
}

func WithWorkDir(dir string) Option {
	return func(c *Config) {
		if c.FilesystemConfig == nil {
			c.FilesystemConfig = &FilesystemConfig{}
		}
		c.FilesystemConfig.WorkDir = dir
		c.FilesystemConfig.localFromOption = true
	}
}

func WithTools(items ...einotool.BaseTool) Option {
	return func(c *Config) {
		for _, item := range items {
			c.ToolDescriptors = append(c.ToolDescriptors, tools.Describe(item))
		}
	}
}

func WithToolMask(mask tools.Mask) Option {
	return func(c *Config) {
		c.ToolMask = mask
	}
}

func WithSubAgents(agents ...*SubAgent) Option {
	return func(c *Config) {
		c.SubAgents = append(c.SubAgents, agents...)
	}
}

func WithSubAgentsDir(dir string) Option {
	return WithSubAgentsDirs(dir)
}

func WithSubAgentsDirs(dirs ...string) Option {
	return func(c *Config) {
		c.SubAgentsDirs = append(c.SubAgentsDirs, dirs...)
	}
}

func WithSubAgentContextInjector(i SubAgentContextInjector) Option {
	return func(c *Config) {
		c.SubAgentContextInjector = i
	}
}

func WithSubAgentTaskStreaming() Option {
	return func(c *Config) {
		c.EnableSubAgentTaskStreaming = true
	}
}

func WithSkillLoader(loader backend.SkillLoader) Option {
	return func(c *Config) {
		c.SkillLoader = loader
	}
}

func WithPlanMiddleware(cfg *middleware.PlanMiddlewareConfig) Option {
	return func(c *Config) {
		c.Middlewares = append(c.Middlewares, middleware.NewPlan(cfg))
	}
}

func WithFilesystem() (option Option) {
	option = func(c *Config) {
		if c.FilesystemConfig == nil {
			c.FilesystemConfig = &FilesystemConfig{}
		}
	}
	return option
}

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

func WithDisableSubAgent() Option {
	return func(c *Config) {
		c.DisableSubAgent = true
	}
}

func WithDisableUploadDownload() Option {
	return func(c *Config) {
		if c.FilesystemConfig == nil {
			c.FilesystemConfig = &FilesystemConfig{}
		}
		c.FilesystemConfig.DisableUploadDownload = true
	}
}

func WithDisableExecute() Option {
	return func(c *Config) {
		if c.FilesystemConfig == nil {
			c.FilesystemConfig = &FilesystemConfig{}
		}
		c.FilesystemConfig.DisableExecute = true
	}
}

func WithBackend(b backend.ToolWorkspace) Option { return WithWorkspace(b) }

func WithWorkspace(ws backend.ToolWorkspace) Option {
	return func(c *Config) { c.Workspace = ws }
}

func WithSandboxBackend(b backend.ToolWorkspace) Option {
	return WithWorkspace(b)
}

func WithPatchToolCalls() Option {
	return func(c *Config) {
		c.EnablePatchToolCalls = true
	}
}

func WithStreamToolCall() Option {
	return func(c *Config) {
		c.EnableStreamToolCall = true
	}
}

func WithWeb() (option Option) {
	option = func(c *Config) {
		c.WebConfig = tools.DefaultWebConfig()
	}
	return option
}

func WithWebConfig(config *tools.WebConfig) (option Option) {
	option = func(c *Config) {
		if config == nil {
			c.WebConfig = tools.DefaultWebConfig()
			return
		}
		cloned := *config
		c.WebConfig = &cloned
	}
	return option
}

func WithMiddleware(m middleware.Middleware) Option {
	return func(c *Config) {
		c.Middlewares = append(c.Middlewares, m)
	}
}

func WithHooks(hooks ...hook.Hooks) Option {
	return func(c *Config) {
		c.Hooks = append(c.Hooks, hooks...)
	}
}

func WithDefaultCallbacks(handlers ...callbacks.Handler) Option {
	return func(c *Config) {
		for _, handler := range handlers {
			if handler != nil {
				c.Callbacks = append(c.Callbacks, handler)
			}
		}
	}
}

func WithCheckpointStore(store compose.CheckPointStore) Option {
	return func(c *Config) {
		c.CheckpointStore = store
	}
}

func WithInterruptBeforeNodes(nodes ...string) Option {
	return func(c *Config) {
		c.InterruptBeforeNodes = append(c.InterruptBeforeNodes, nodes...)
	}
}

func WithInterruptAfterNodes(nodes ...string) Option {
	return func(c *Config) {
		c.InterruptAfterNodes = append(c.InterruptAfterNodes, nodes...)
	}
}

func WithAllFeatures() (option Option) {
	option = func(c *Config) {
		if c.FilesystemConfig == nil {
			c.FilesystemConfig = &FilesystemConfig{}
		}
		c.EnablePatchToolCalls = true
		c.WebConfig = tools.DefaultWebConfig()
	}
	return option
}

func WithHITLConfig(cfg *HITLConfig) Option {
	return func(c *Config) {
		c.HITLConfig = cfg
	}
}

func WithToolInfoRewriter(rewriter tools.ToolInfoRewriter) Option {
	return func(c *Config) {
		c.ToolInfoRewriter = rewriter
	}
}

func WithToolNodePreHandler(handler ToolNodePreHandler) Option {
	return func(c *Config) {
		c.ToolNodePreHandler = handler
	}
}

func WithToolNodePostHandler(handler ToolNodePostHandler) Option {
	return func(c *Config) {
		c.ToolNodePostHandler = handler
	}
}
