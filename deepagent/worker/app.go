package worker

import (
	"context"
	"fmt"
	"strconv"

	dalcache "eino-cli/deepagent/dal/cache"
	daldb "eino-cli/deepagent/dal/db"
	checkpointer "eino-cli/deepagent/graph/checkpoint"
	"eino-cli/deepagent/graph/computer"
	"eino-cli/deepagent/graph/mcp"
	"eino-cli/deepagent/graph/modelhub"
	skillspkg "eino-cli/deepagent/graph/skills"
	"eino-cli/deepagent/manager"
	messagepkg "eino-cli/deepagent/message"
	"eino-cli/deepagent/threadhost"

	modelpkg "github.com/cloudwego/eino/components/model"
)

// Run owns process-wide resources and starts the canonical distributed Worker.
func Run(ctx context.Context, cfg Config) error {
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
	host := &threadhost.ThreadHost{
		Config: cfg.Host,
		Client: coordinator,
		Runtime: threadhost.RuntimeConfig{
			FilesystemKind: cfg.FilesystemKind, Docker: cfg.Docker,
			BrowserOrigins: cfg.BrowserOrigins, ComputerApps: cfg.ComputerApps,
			Models: models, DefaultModel: cfg.DefaultModel,
			SystemPrompt: cfg.SystemPrompt, MaxSteps: cfg.MaxSteps, MaxModelCalls: cfg.MaxModelCalls,
			ContextWindow: cfg.ContextWindow, CompactThresholdTokens: cfg.CompactThresholdTokens,
			KeepRecentMessages: cfg.KeepRecentMessages, Web: cfg.Web,
			MemoryEnabled: cfg.MemoryEnabled, MemoryDir: cfg.MemoryDir,
			MemoryUserID: cfg.MemoryUserID, MemoryLeaseTTL: cfg.MemoryLeaseTTL,
		},
		Deps: threadhost.RuntimeDeps{
			Desktop:        desktop,
			ConversationDB: conversationDAO, Checkpoint: checkpointStore, Tools: mcpTools, SkillLoader: skillLoader,
			MemoryStore: coordinator, Collaboration: coordinator,
			IsToolAlwaysAllowed: coordinator.IsToolAlwaysAllowed,
			GenerateMessageID: func(idCtx context.Context, _ *messagepkg.Message) (string, error) {
				id, err := dalcache.GenerateID(idCtx, redisClient)
				if err != nil {
					return "", err
				}
				return strconv.FormatInt(id, 10), nil
			},
		},
	}
	return host.Run(ctx)
}
