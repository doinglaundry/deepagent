package app

import (
	"context"
	"log/slog"

	"eino-cli/backend/cli/render"
	"eino-cli/backend/cli/repl"
	"eino-cli/backend/config"

	"eino-cli/backend/runtime/eino"

	"eino-cli/backend/tools/registry"
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

	runtime, err := eino.BuildRuntime(context.Background(), cfg)
	if err != nil {
		return nil, err
	}

	renderer := render.NewConsoleRenderer(nil)

	return &App{runner: repl.New(cfg, renderer, runtime, registry.New(), opts.KnownCommands)}, nil
}

func (a *App) Run() error {
	return a.runner.Run(context.Background())
}
