package execution

import (
	"context"

	"eino-cli/deepagent/graph/tools"
	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
)

type Config struct {
	LocalModelParametersFingerprint string
	SubAgents                       []*SubAgent
	SkillLoader                     agentmodel.SkillLoader
	FilesystemConfig                *tools.FilesystemConfig
	WebConfig                       *tools.WebConfig
	Filesystem                      agentmodel.ToolFilesystem
	Callbacks                       []callbacks.Handler
	Middlewares                     []agentmodel.Middleware
	EnableEagerTools                bool
	Model                           model.ToolCallingChatModel
	ToolDescriptors                 []agentmodel.ToolDescriptor
	ToolMask                        agentmodel.Mask
	ReadOnlyToolsOnly               bool
	Policy                          agentmodel.Policy
	Parallelism                     int
	MaxSteps                        int
	MaxModelCalls                   int
	Name                            string
	Depth                           int
	ThreadID                        string
	RunID                           string
	Prompts                         []*agentmodel.Message
	Conversation                    agentmodel.Conversation
	Emit                            func(context.Context, agentmodel.RuntimeEvent) error
	DrainInput                      func(context.Context, string) ([]agentmodel.RunInput, bool, error)
	CheckpointStore                 compose.CheckPointStore
}

// RunOptions 只描述本次执行的回调和恢复位置；输入身份直接放在 RunInput 中。
type RunOptions struct {
	Callbacks           []callbacks.Handler
	CheckpointID        string
	WriteToCheckpointID string
	ForceNewRun         bool
	ResumeInterruptIDs  []string
	ResumeData          map[string]any
}

func (config *Config) Clone() (cloned *Config) {
	if config == nil {
		return &Config{}
	}
	value := *config
	cloned = &value
	cloned.ToolDescriptors = append([]agentmodel.ToolDescriptor(nil), config.ToolDescriptors...)
	cloned.Prompts = append([]*agentmodel.Message(nil), config.Prompts...)
	cloned.SubAgents = append([]*SubAgent(nil), config.SubAgents...)
	cloned.Middlewares = append([]agentmodel.Middleware(nil), config.Middlewares...)
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
