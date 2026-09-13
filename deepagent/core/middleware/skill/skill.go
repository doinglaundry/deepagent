package skill

import (
	"context"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/tool"
)

type Loader interface {
	Load(context.Context, string) (string, error)
}
type MiddlewareConfig struct{ ToolMask tools.Mask }
type Middleware struct {
	middleware.BaseMiddleware
	loader Loader
}

func New(loader Loader) middleware.Middleware                                { return &Middleware{loader: loader} }
func NewWithConfig(loader Loader, _ *MiddlewareConfig) middleware.Middleware { return New(loader) }
func (m *Middleware) Name() string                                           { return "skill" }
func (m *Middleware) Tools(context.Context) ([]tool.BaseTool, error)         { return nil, nil }
