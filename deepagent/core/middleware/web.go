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
func (m *Web) Tools(ctx context.Context) ([]tool.BaseTool, error) {
	if m == nil {
		return nil, nil
	}
	items, err := tools.NewWebTools(m.cfg)
	if err != nil {
		return nil, err
	}
	if m.cfg == nil || m.cfg.ToolMask == nil {
		return items, nil
	}
	filtered := make([]tool.BaseTool, 0, len(items))
	for _, item := range items {
		info, err := item.Info(ctx)
		if err != nil {
			return nil, err
		}
		if !m.cfg.ToolMask(ctx, info) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered, nil
}
