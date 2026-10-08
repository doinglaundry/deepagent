package run

import (
	"context"

	"eino-cli/deepagent/graph/execution"
	"eino-cli/deepagent/graph/types"
	messagepkg "eino-cli/deepagent/message"

	"github.com/cloudwego/eino/components/model"
)

type Config struct {
	Graph            execution.Config
	EnablePlan       bool
	EventIDProvider  func(context.Context, string, string) string
	RunCompleted     func(context.Context, string, string, model.ToolCallingChatModel, []*messagepkg.Message)
	Events           chan Event
	Resume           *execution.RunOptions
	OnRestoredInputs func(*Run, []types.Input)
	OnFinish         func(context.Context, *Run, error) error
}

func (c *Config) Clone() *Config {
	if c == nil {
		return &Config{}
	}
	out := *c
	out.Graph = *c.Graph.Clone()
	return &out
}
