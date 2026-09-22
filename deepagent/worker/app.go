package worker

import (
	"context"
	"fmt"
	"strconv"

	"eino-cli/deepagent/core/agentthread"
	"eino-cli/deepagent/core/checkpoint"
	"eino-cli/deepagent/core/mcp"
	skillmw "eino-cli/deepagent/core/middlewares/skill"
	"eino-cli/deepagent/core/modelhub"
	"eino-cli/deepagent/manager"
	threadpkg "eino-cli/deepagent/thread"
	"eino-cli/deepagent/threadhost"
	modelpkg "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Run owns process-wide resources and starts the canonical distributed Worker.
func Run(ctx context.Context, cfg Config) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	coordinator, err := manager.Open(ctx, cfg.Manager)
	if err != nil {
		return fmt.Errorf("create Manager: %w", err)
	}
	redisClient := coordinator.Redis()
	sqlDB := coordinator.DB().DB(ctx, true)

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
	skillLoader, err := skillmw.Load(cfg.SkillPaths)
	if err != nil {
		return fmt.Errorf("load skills: %w", err)
	}

	checkpointStore, err := checkpointer.NewRaw(redisClient, "deepagent:checkpoint")
	if err != nil {
		return fmt.Errorf("initialize checkpoints: %w", err)
	}
	history := agentthread.NewGormHistoryRolloutStore(sqlDB, cfg.HistoryTable,
		func(idCtx context.Context, _, _ string) int64 {
			id, _ := manager.IDNextSharedID(idCtx, redisClient)
			return id
		},
		agentthread.NewRedisSeqGenerator(redisClient, "deepagent:history:seq"),
	)
	if err = history.AutoMigrate(ctx); err != nil {
		return fmt.Errorf("migrate thread history: %w", err)
	}

	host := &threadhost.ThreadHost{
		Config: cfg.Host,
		Client: coordinator,
		Runtime: threadhost.RuntimeConfig{
			Models: models, DefaultModel: cfg.DefaultModel, RoleModels: cfg.RoleModels,
			SystemPrompt: cfg.SystemPrompt, MaxSteps: cfg.MaxSteps, MaxModelCalls: cfg.MaxModelCalls,
			ContextWindow: cfg.ContextWindow, CompactThresholdTokens: cfg.CompactThresholdTokens,
			KeepRecentMessages: cfg.KeepRecentMessages, Web: cfg.Web,
			MemoryEnabled: cfg.MemoryEnabled, MemoryDir: cfg.MemoryDir,
			MemoryUserID: cfg.MemoryUserID, MemoryLeaseTTL: cfg.MemoryLeaseTTL,
		},
		Deps: threadhost.RuntimeDeps{
			History: history, Checkpoint: checkpointStore, Tools: mcpTools, SkillLoader: skillLoader,
			MemoryStore: coordinator, Collaboration: coordinator,
			HistoryRecordID: func(idCtx context.Context, _, _ string, message *schema.Message) int64 {
				if id, parseErr := strconv.ParseInt(threadpkg.MessageID(message), 10, 64); parseErr == nil && id > 0 {
					return id
				}
				id, _ := manager.IDNextSharedID(idCtx, redisClient)
				return id
			},
		},
	}
	return host.Run(ctx)
}
