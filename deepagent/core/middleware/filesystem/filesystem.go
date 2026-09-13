package filesystem

import (
	"context"
	"eino-cli/deepagent/core/backends"
	"eino-cli/deepagent/core/middleware"
	"eino-cli/deepagent/core/tools"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type FilesystemConfig struct {
	Backend                                                            backends.Backend
	ReadOnly, DisableUploadDownload, DisableExecute, DisableApplyPatch bool
	WorkDir                                                            string
	CommandTimeout                                                     interface{}
	ToolMask                                                           tools.Mask
}
type filesystemMiddleware struct {
	middleware.BaseMiddleware
	cfg *FilesystemConfig
}

func New(cfg *FilesystemConfig) middleware.Middleware { return &filesystemMiddleware{cfg: cfg} }
func (m *filesystemMiddleware) Name() string          { return "filesystem" }
func (m *filesystemMiddleware) BuildPrompt(context.Context) ([]*schema.Message, error) {
	return nil, nil
}
func (m *filesystemMiddleware) Tools(context.Context) ([]tool.BaseTool, error) { return nil, nil }
