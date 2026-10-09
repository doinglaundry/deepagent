package run

import (
	"context"

	"eino-cli/deepagent/graph/execution"
	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/model"
)

type Config struct {
	Graph            execution.Config
	EnablePlan       bool
	EventIDProvider  func(context.Context, string, string) string
	RunCompleted     func(context.Context, string, string, model.ToolCallingChatModel, []*agentmodel.Message)
	Events           chan agentmodel.RunEvent
	Resume           *execution.RunOptions
	OnRestoredInputs func(*Run, []agentmodel.RunInput)
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
