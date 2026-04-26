package app

import (
	"context"
	"fmt"
	"log/slog"

	"eino-cli/internal/cli/render"
	"eino-cli/internal/cli/repl"
	"eino-cli/internal/config"
	memorypolicy "eino-cli/internal/memory/policy"
	memorystore "eino-cli/internal/memory/store"
	"eino-cli/internal/orchestrator"
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

type Options struct {
	KnownCommands []string
}

func New(opts Options) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	slog.Info("孙颖宝 cfg: %v", "model", cfg)

	manifest, err := workspace.Discover(cfg.RootDir)
	if err != nil {
		return nil, fmt.Errorf("load workspace: %w", err)
	}

	checkpointStore := checkpoint.NewStore(cfg.CheckpointDir)

	runtime, err := buildRuntime(cfg, checkpointStore)
	if err != nil {
		return nil, err
	}

	persistence := orchestrator.NewPersistence(
		session.NewStore(cfg.SessionsDir),
		checkpointStore,
		memorystore.NewStore(cfg.MemoryDir),
		memorypolicy.New(),
	)
	service := orchestrator.NewService(runtime, registry.New(), execute.New(), policy.New()).WithPersistence(persistence)
	renderer := render.NewConsoleRenderer(nil)

	slog.Info("孙颖宝 app ready")
	return &App{runner: repl.New(cfg, manifest, renderer, service, opts.KnownCommands)}, nil
}

func (a *App) Run() error {
	return a.runner.Run(context.Background())
}

func buildRuntime(cfg config.Config, checkpointStore *checkpoint.Store) (eino.Runtime, error) {
	runtime, err := eino.BuildRuntime(context.Background(), cfg, checkpointStore)
	if err != nil {
		return nil, fmt.Errorf("init deep runtime: %w", err)
	}
	return runtime, nil
}
