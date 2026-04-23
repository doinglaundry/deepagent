package app

import (
	"context"

	"eino-cli/internal/cli/render"
	"eino-cli/internal/cli/repl"
	"eino-cli/internal/config"
	"eino-cli/internal/orchestrator"
	"eino-cli/internal/runtime/eino"
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
		return nil, err
	}

	runtime := eino.NewNoopRuntime("noop-model")
	service := orchestrator.NewService(runtime, registry.New(), execute.New(), policy.New())
	renderer := render.NewConsoleRenderer(nil)

	return &App{runner: repl.New(cfg, manifest, renderer, service)}, nil
}

func (a *App) Run() error {
	return a.runner.Run(context.Background())
}
