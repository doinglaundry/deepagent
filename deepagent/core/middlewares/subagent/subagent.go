package subagent

import (
	"context"
	"eino-cli/deepagent/core/middlewares"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type SubAgent struct {
	Name, SystemPrompt                    string
	MaxSteps                              int
	EnableFilesystem, EnableWeb, ReadOnly bool
	ToolMask                              tools.Mask
	Tools                                 []tool.BaseTool
}
type SubAgentConfig struct {
	SubAgents                      []*SubAgent
	DefaultModel                   model.ToolCallingChatModel
	DefaultTools                   []tool.BaseTool
	Factory                        SubAgentFactory
	SubAgentSkillMiddlewareFactory func() middleware.Middleware
	ContextInjector                SubAgentContextInjector
	ToolMask                       tools.Mask
	EnableTaskStreaming            bool
}
type SubAgentContextInjector func(context.Context, string) ([]*schema.Message, error)
type SubAgentFactory func(context.Context, model.ToolCallingChatModel, *SubAgent, []tool.BaseTool, []middleware.Middleware) (SubAgentRunner, error)
type SubAgentRunOptions struct{ CheckpointID string }
type SubAgentRunOption func(*SubAgentRunOptions)

func ApplySubAgentRunOptions(opts ...SubAgentRunOption) SubAgentRunOptions {
	var o SubAgentRunOptions
	for _, f := range opts {
		f(&o)
	}
	return o
}

type SubAgentRunner interface {
	Run(context.Context, []*schema.Message, ...SubAgentRunOption) (*schema.Message, error)
	Stream(context.Context, []*schema.Message, ...SubAgentRunOption) (*schema.StreamReader[*schema.Message], error)
	Close(context.Context) error
	Depth() int
}
type Middleware struct {
	middleware.BaseMiddleware
	cfg *SubAgentConfig
}

func New(cfg *SubAgentConfig) middleware.Middleware                               { return &Middleware{cfg: cfg} }
func (m *Middleware) Name() string                                                { return "subagent" }
func LoadSubAgentsFromDir(context.Context, string, any, any) ([]*SubAgent, error) { return nil, nil }
