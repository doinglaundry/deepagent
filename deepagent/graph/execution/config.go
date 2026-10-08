package execution

import (
	"context"
	"time"

	"eino-cli/deepagent/graph/conversation"
	filesystempkg "eino-cli/deepagent/graph/filesystem"
	"eino-cli/deepagent/graph/middleware"
	skillspkg "eino-cli/deepagent/graph/skills"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
)

type Config struct {
	SubAgents         []*SubAgent
	SkillLoader       skillspkg.SkillLoader
	FilesystemConfig  *FilesystemConfig
	WebConfig         *tools.WebConfig
	Filesystem        filesystempkg.ToolFilesystem
	Callbacks         []callbacks.Handler
	Middlewares       []middleware.Middleware
	EnableEagerTools  bool
	Model             model.ToolCallingChatModel
	ToolDescriptors   []tools.ToolDescriptor
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
	Prompts           []*messagepkg.Message
	Conversation      conversation.IConversation
	Emit              func(context.Context, types.RuntimeEvent) error
	DrainInput        func(context.Context, string) ([]types.Input, bool, error)
	CheckpointStore   compose.CheckPointStore
}

type Option func(*Config)

func WithConfig(source *Config) Option { return func(config *Config) { *config = *source.Clone() } }

func WithModel(chatModel model.ToolCallingChatModel) Option {
	return func(config *Config) { config.Model = chatModel }
}

type RunOptions struct {
	InputIDs            []string
	InputMeta           []any
	CheckpointID        string
	WriteToCheckpointID string
	ForceNewRun         bool
	ResumeInterruptIDs  []string
	ResumeData          map[string]any
	composeOpts         []compose.Option
}

type RunOptionFunc func(*RunOptions)

func WithCallbacks(handlers ...callbacks.Handler) RunOptionFunc {
	return func(runOptions *RunOptions) {
		runOptions.composeOpts = append(runOptions.composeOpts, compose.WithCallbacks(handlers...))
	}
}

func WithCheckpointID(id string) RunOptionFunc {
	return func(runOptions *RunOptions) { runOptions.CheckpointID = id }
}

func WithWriteToCheckpointID(id string) RunOptionFunc {
	return func(runOptions *RunOptions) { runOptions.WriteToCheckpointID = id }
}

func WithForceNewRun() RunOptionFunc {
	return func(runOptions *RunOptions) { runOptions.ForceNewRun = true }
}

func WithResume(ids ...string) RunOptionFunc {
	return func(runOptions *RunOptions) {
		runOptions.ResumeInterruptIDs = append(runOptions.ResumeInterruptIDs, ids...)
	}
}

func WithResumeData(data map[string]any) RunOptionFunc {
	return func(runOptions *RunOptions) {
		if runOptions.ResumeData == nil {
			runOptions.ResumeData = make(map[string]any)
		}
		for id, value := range data {
			runOptions.ResumeData[id] = value
		}
	}
}

func WithInputIDs(ids ...string) RunOptionFunc {
	return func(runOptions *RunOptions) { runOptions.InputIDs = append([]string(nil), ids...) }
}

func WithInputMetadata(meta ...any) RunOptionFunc {
	return func(runOptions *RunOptions) { runOptions.InputMeta = append([]any(nil), meta...) }
}

type FilesystemConfig struct {
	ReadOnly              bool
	DisableUploadDownload bool
	DisableExecute        bool
	DisableApplyPatch     bool
	CommandTimeout        time.Duration
}

func (config *Config) Clone() (cloned *Config) {
	if config == nil {
		return &Config{}
	}
	value := *config
	cloned = &value
	cloned.ToolDescriptors = append([]tools.ToolDescriptor(nil), config.ToolDescriptors...)
	cloned.Prompts = append([]*messagepkg.Message(nil), config.Prompts...)
	cloned.SubAgents = append([]*SubAgent(nil), config.SubAgents...)
	cloned.Middlewares = append([]middleware.Middleware(nil), config.Middlewares...)
	cloned.Callbacks = append([]callbacks.Handler(nil), config.Callbacks...)

	if config.FilesystemConfig != nil {
		filesystem := *config.FilesystemConfig
		cloned.FilesystemConfig = &filesystem
	}
	if config.WebConfig != nil {
		webConfig := *config.WebConfig
		cloned.WebConfig = &webConfig
	}

	return cloned
}

func WithMaxSteps(maxSteps int) Option {
	return func(config *Config) {
		config.MaxSteps = maxSteps
	}
}

func WithMaxModelCalls(maxModelCalls int) Option {
	return func(config *Config) {
		config.MaxModelCalls = maxModelCalls
	}
}

func WithTools(toolDescriptors ...tools.ToolDescriptor) Option {
	return func(config *Config) {
		config.ToolDescriptors = append(config.ToolDescriptors, toolDescriptors...)
	}
}

func WithToolMask(toolMask tools.Mask) Option {
	return func(config *Config) {
		config.ToolMask = toolMask
	}
}

func WithSubAgents(subAgents ...*SubAgent) Option {
	return func(config *Config) {
		config.SubAgents = append(config.SubAgents, subAgents...)
	}
}

func WithSkillLoader(skillLoader skillspkg.SkillLoader) Option {
	return func(config *Config) {
		config.SkillLoader = skillLoader
	}
}

// WithPlan enables the plan tool and its prompt independently.
func WithPlan(onUpdate tools.PlanUpdateHandler) Option {
	return func(config *Config) {
		config.ToolDescriptors = append(config.ToolDescriptors, tools.NewUpdatePlanTool(onUpdate))
		config.Middlewares = append(config.Middlewares, middleware.NewPlan())
	}
}

func WithFilesystem(filesystem filesystempkg.ToolFilesystem) Option {
	return func(config *Config) {
		config.Filesystem = filesystem
		if config.FilesystemConfig == nil {
			config.FilesystemConfig = &FilesystemConfig{}
		}
	}
}

func WithFilesystemConfig(filesystemConfig *FilesystemConfig) (option Option) {
	option = func(config *Config) {
		if filesystemConfig == nil {
			config.FilesystemConfig = &FilesystemConfig{}
			return
		}
		cloned := *filesystemConfig
		config.FilesystemConfig = &cloned
	}
	return option
}

func WithDisableUploadDownload() Option {
	return func(config *Config) {
		if config.FilesystemConfig == nil {
			config.FilesystemConfig = &FilesystemConfig{}
		}
		config.FilesystemConfig.DisableUploadDownload = true
	}
}

func WithDisableExecute() Option {
	return func(config *Config) {
		if config.FilesystemConfig == nil {
			config.FilesystemConfig = &FilesystemConfig{}
		}
		config.FilesystemConfig.DisableExecute = true
	}
}

func WithEagerTools() Option {
	return func(config *Config) {
		config.EnableEagerTools = true
	}
}

func WithWeb() (option Option) {
	option = func(config *Config) {
		config.WebConfig = tools.NewDefaultWebConfig()
	}
	return option
}

func WithWebConfig(webConfig *tools.WebConfig) (option Option) {
	option = func(config *Config) {
		if webConfig == nil {
			config.WebConfig = tools.NewDefaultWebConfig()
			return
		}
		cloned := *webConfig
		config.WebConfig = &cloned
	}
	return option
}

func WithMiddleware(currentMiddleware middleware.Middleware) Option {
	return func(config *Config) {
		config.Middlewares = append(config.Middlewares, currentMiddleware)
	}
}

func WithDefaultCallbacks(handlers ...callbacks.Handler) Option {
	return func(config *Config) {
		for _, handler := range handlers {
			if handler != nil {
				config.Callbacks = append(config.Callbacks, handler)
			}
		}
	}
}

func WithCheckpointStore(checkpointStore compose.CheckPointStore) Option {
	return func(config *Config) {
		config.CheckpointStore = checkpointStore
	}
}

func WithAllFeatures() (option Option) {
	option = func(config *Config) {
		if config.FilesystemConfig == nil {
			config.FilesystemConfig = &FilesystemConfig{}
		}
		config.WebConfig = tools.NewDefaultWebConfig()
	}
	return option
}
