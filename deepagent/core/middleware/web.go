package middleware

import (
	"context"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/tool"
)

type Web struct {
	BaseMiddleware
	cfg *tools.WebConfig
}

func NewWeb(cfg *tools.WebConfig) Middleware { return &Web{cfg: cfg} }
func (m *Web) Name() string                  { return "web" }
func (m *Web) Tools(context.Context) ([]tool.BaseTool, error) {
	if m == nil {
		return nil, nil
	}
	return tools.NewWebTools(m.cfg)
}
