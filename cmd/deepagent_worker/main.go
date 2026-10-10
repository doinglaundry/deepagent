package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"syscall"

	"eino-cli/deepagent/appconfig"
	dalcache "eino-cli/deepagent/dal/cache"
	daldb "eino-cli/deepagent/dal/db"
	checkpointer "eino-cli/deepagent/graph/checkpoint"
	"eino-cli/deepagent/graph/computer"
	"eino-cli/deepagent/graph/mcp"
	"eino-cli/deepagent/graph/modelhub"
	skillspkg "eino-cli/deepagent/graph/skills"
	"eino-cli/deepagent/graph/tools"
	"eino-cli/deepagent/localmodel"
	"eino-cli/deepagent/manager"
	agentmodel "eino-cli/deepagent/model"
	"eino-cli/deepagent/worker"

	modelpkg "github.com/cloudwego/eino/components/model"
)

func main() {
	config := flag.String("config", "yaml/deepagent.yaml", "Worker YAML with shared Manager, models, and execution settings")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	c, err := appconfig.Load(*config)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("starting distributed Worker", "default_model", c.DefaultModel)
	err = runWorker(ctx, c)
	expectedShutdown := ctx.Err() != nil && errors.Is(err, context.Canceled)
	if err != nil && !expectedShutdown {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runWorker owns process-wide resources and starts the canonical distributed Worker.
func runWorker(ctx context.Context, cfg appconfig.Config) error {
	err := cfg.Validate()
	if err != nil {
		return err
	}
	coordinator, err := manager.Open(ctx, cfg.Manager)
	if err != nil {
		return fmt.Errorf("create Manager: %w", err)
	}
	redisClient := coordinator.Redis()

	models := make(map[string]modelpkg.ToolCallingChatModel, len(cfg.Models))
	for _, modelConfig := range cfg.Models {
		client, createErr := modelhub.New(ctx, modelConfig)
		if createErr != nil {
			return fmt.Errorf("model %s: %w", modelConfig.Name, createErr)
		}
		models[modelConfig.Name] = client
	}
	mcpTools, err := mcp.LoadMCP(ctx, cfg.MCP)
	if err != nil {
		return fmt.Errorf("initialize MCP: %w", err)
	}
	defer mcp.CloseMCP(mcpTools)
	skillLoader, err := skillspkg.LoadSkills(cfg.SkillPaths)
	if err != nil {
		return fmt.Errorf("load skills: %w", err)
	}

	checkpointStore, err := checkpointer.NewRedisStore(redisClient, "deepagent:checkpoint")
	if err != nil {
		return fmt.Errorf("initialize checkpoints: %w", err)
	}
	conversationDAO := daldb.NewConversationDAO(coordinator.DB(), cfg.HistoryTable, redisClient)
	err = conversationDAO.MigrateSchema(ctx)
	if err != nil {
		return fmt.Errorf("migrate thread history: %w", err)
	}

	var desktop *computer.Desktop
	if cfg.ComputerEnabled {
		desktop, err = computer.NewDesktop(ctx)
		if err != nil {
			return err
		}
		defer desktop.Close(context.WithoutCancel(ctx))
	}
	var localModelService *localmodel.Service
	toolDescriptors := append([]agentmodel.ToolDescriptor(nil), mcpTools...)
	if cfg.LocalModel != nil {
		localModelDAO := daldb.NewLocalModelDAO(coordinator.DB(), cfg.LocalModel.ModelName)
		err = localModelDAO.MigrateSchema(ctx)
		if err != nil {
			return err
		}
		localModelService, err = localmodel.New(context.WithoutCancel(ctx), *cfg.LocalModel, localModelDAO)
		if err != nil {
			return err
		}
		defer localModelService.Close()
		toolDescriptors = append(toolDescriptors, tools.NewLocalModelTool(localModelService))
	}
	agentWorker := &worker.Worker{
		LocalModel: localModelService,
		Config:     cfg.Worker,
		Client:     coordinator,
		Runtime: worker.RuntimeConfig{
			FilesystemKind: cfg.FilesystemKind, Docker: cfg.Docker,
			BrowserOrigins: cfg.BrowserOrigins, ComputerApps: cfg.ComputerApps,
			Models: models, DefaultModel: cfg.DefaultModel,
			SystemPrompt: cfg.SystemPrompt, MaxSteps: cfg.MaxSteps, MaxModelCalls: cfg.MaxModelCalls,
			ContextWindow: cfg.ContextWindow, CompactThresholdTokens: cfg.CompactThresholdTokens,
			KeepRecentMessages: cfg.KeepRecentMessages, Web: cfg.Web,
			MemoryEnabled: cfg.MemoryEnabled, MemoryDir: cfg.MemoryDir,
			MemoryUserID: cfg.MemoryUserID, MemoryLeaseTTL: cfg.MemoryLeaseTTL,
		},
		Deps: worker.RuntimeDeps{
			Desktop:        desktop,
			ConversationDB: conversationDAO, Checkpoint: checkpointStore, Tools: toolDescriptors, SkillLoader: skillLoader,
			MemoryStore: coordinator, Collaboration: coordinator,
			IsToolAlwaysAllowed: coordinator.IsToolAlwaysAllowed,
			GenerateMessageID: func(idCtx context.Context, _ *agentmodel.Message) (string, error) {
				id, err := dalcache.GenerateID(idCtx, redisClient)
				if err != nil {
					return "", err
				}
				return strconv.FormatInt(id, 10), nil
			},
		},
	}
	return agentWorker.Run(ctx)
}
