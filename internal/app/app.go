package app

import (
	"context"
	"fmt"
	"time"

	"eino-cli/internal/cli/render"
	"eino-cli/internal/cli/repl"
	"eino-cli/internal/config"
	memorypolicy "eino-cli/internal/memory/policy"
	memorystore "eino-cli/internal/memory/store"
	"eino-cli/internal/orchestrator"
	"eino-cli/internal/plugin/gateway"
	"eino-cli/internal/runtime/eino"
	"eino-cli/internal/session"
	"eino-cli/internal/session/checkpoint"
	"eino-cli/internal/tools/execute"
	"eino-cli/internal/tools/policy"
	"eino-cli/internal/tools/registry"
	"eino-cli/internal/workspace"
)

type App struct {
	runner repl.Runner
}

func New() (*App, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, err
	}

	manifest, err := workspace.Discover(cfg.RootDir)
	if err != nil {
		return nil, fmt.Errorf("load workspace: %w", err)
	}

	pluginGateway := gateway.New()
	if err := pluginGateway.Check(); err != nil {
		// MVP 阶段插件不可用不阻断主链路，保留显式检查作为 UX 提示边界。
	}

	runtime := buildRuntime(cfg)
	persistence := orchestrator.NewPersistence(
		session.NewStore(cfg.SessionsDir),
		checkpoint.NewStore(cfg.CheckpointDir),
		memorystore.NewStore(cfg.MemoryDir),
		memorypolicy.New(),
	)
	service := orchestrator.NewService(runtime, registry.New(), execute.New(), policy.New()).WithPersistence(persistence)
	renderer := render.NewConsoleRenderer(nil)

	return &App{runner: repl.New(cfg, manifest, renderer, service)}, nil
}

func (a *App) Run() error {
	return a.runner.Run(context.Background())
}

func buildRuntime(cfg config.Config) eino.Runtime {
	return eino.NewLocalServiceRuntime(cfg.RuntimeBaseURL, cfg.RuntimeModel, time.Duration(cfg.RuntimeTimeout)*time.Second)
}
