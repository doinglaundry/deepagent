package app

import (
	"context"
	"log/slog"

	"eino-cli/internal/cli/render"
	"eino-cli/internal/cli/repl"
	"eino-cli/internal/config"
	memorystore "eino-cli/internal/memory/store"
	"eino-cli/internal/runtime/eino"
	"eino-cli/internal/session"
	"eino-cli/internal/session/checkpoint"
	"eino-cli/internal/tools/registry"
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

	checkpointStore := checkpoint.NewStore(cfg.CheckpointDir)

	runtime, err := eino.BuildRuntime(context.Background(), cfg, checkpointStore)
	if err != nil {
		return nil, err
	}

	renderer := render.NewConsoleRenderer(nil)

	return &App{runner: repl.New(cfg, renderer, runtime, registry.New(), opts.KnownCommands)}, nil
}

func (a *App) Run() error {
	return a.runner.Run(context.Background())
}
