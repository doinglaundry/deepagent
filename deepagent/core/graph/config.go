package graph

import (
	"context"
	"time"

	"eino-cli/deepagent/core/backend"
	"github.com/cloudwego/eino/callbacks"

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
	SubAgents         []*SubAgent
	SkillLoader       backend.SkillLoader
	FilesystemConfig  *FilesystemConfig
	WebConfig         *tools.WebConfig
	Filesystem        backend.ToolFilesystem
	Callbacks         []callbacks.Handler
	Middlewares       []middleware.Middleware
	EnableEagerTools  bool
	Model             model.ToolCallingChatModel
	ToolDescriptors   []tools.Descriptor
	ToolMask          tools.Mask
	ReadOnlyToolsOnly bool
	Policy            tools.Policy
	Parallelism       int
	MaxSteps          int
	MaxModelCalls     int
	Name              string
	Depth             int
	ThreadID          string
	RunID             string
	Prompts           []*schema.Message
	Conversation      Conversation
	Emit              func(context.Context, types.RuntimeEvent) error
	DrainInput        func(context.Context, string) ([]types.Input, bool, error)
	CheckpointStore   compose.CheckPointStore
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

type FilesystemConfig struct {
	ReadOnly              bool
	DisableUploadDownload bool
	DisableExecute        bool
	DisableApplyPatch     bool
	CommandTimeout        time.Duration
}

func (c *Config) Clone() (cloned *Config) {
	if c == nil {
		return &Config{}
	}
	value := *c
	cloned = &value
	cloned.ToolDescriptors = append([]tools.Descriptor(nil), c.ToolDescriptors...)
	cloned.Prompts = append([]*schema.Message(nil), c.Prompts...)
	cloned.SubAgents = append([]*SubAgent(nil), c.SubAgents...)
	cloned.Middlewares = append([]middleware.Middleware(nil), c.Middlewares...)
	cloned.Callbacks = append([]callbacks.Handler(nil), c.Callbacks...)

	if c.FilesystemConfig != nil {
		filesystem := *c.FilesystemConfig
		cloned.FilesystemConfig = &filesystem
	}
	if c.WebConfig != nil {
		webConfig := *c.WebConfig
		cloned.WebConfig = &webConfig
	}

	return cloned
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

func WithFilesystem(filesystem backend.ToolFilesystem) Option {
	return func(c *Config) {
		c.Filesystem = filesystem
		if c.FilesystemConfig == nil {
			c.FilesystemConfig = &FilesystemConfig{}
		}
	}
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

func WithEagerTools() Option {
	return func(c *Config) {
		c.EnableEagerTools = true
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

func WithAllFeatures() (option Option) {
	option = func(c *Config) {
		if c.FilesystemConfig == nil {
			c.FilesystemConfig = &FilesystemConfig{}
		}
		c.WebConfig = tools.DefaultWebConfig()
	}
	return option
}
