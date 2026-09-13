package web

import (
	"context"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/tool"
)

type WebConfig struct {
	ToolMask   tools.Mask
	MaxResults int
	Topic      string
}

func DefaultConfig() *WebConfig { return &WebConfig{MaxResults: 5, Topic: "general"} }

type Middleware struct {
	middleware.BaseMiddleware
	cfg *WebConfig
}

func New(cfg *WebConfig) middleware.Middleware                       { return &Middleware{cfg: cfg} }
func (m *Middleware) Name() string                                   { return "web" }
func (m *Middleware) Tools(context.Context) ([]tool.BaseTool, error) { return nil, nil }
