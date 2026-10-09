package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentmodel "eino-cli/deepagent/model"

	"github.com/cloudwego/eino/components/model"
)

type Config struct {
	Store        agentmodel.MemoryStore
	LeaseTTL     time.Duration
	Root         string
	Model        model.ToolCallingChatModel
	Consolidator func(context.Context, string, string) (string, error)
}

type memoryService struct{ c Config }

func New(config Config) (agentmodel.MemoryService, error) {
	if config.Model == nil || config.Root == "" {
		return nil, errors.New("memory requires model and root")
	}
	if config.LeaseTTL <= 0 {
		config.LeaseTTL = 30 * time.Second
	}
	if config.LeaseTTL < 3*time.Millisecond {
		return nil, errors.New("memory lease TTL must be at least 3ms")
	}
	root, operationErr := filepath.Abs(config.Root)
	if operationErr != nil {
		return nil, operationErr
	}
	config.Root = root
	operationErr = os.MkdirAll(root, 0700)
	if operationErr != nil {
		return nil, operationErr
	}
	if config.Consolidator == nil {
		config.Consolidator = NewAgentConsolidator(config.Model, config.Root)
	}
	return &memoryService{config}, nil
}

func (memoryService *memoryService) Read(ctx context.Context, scope string) (*agentmodel.MemorySnapshot, error) {
	validateScopeErr := validateScope(ctx, scope)
	if validateScopeErr != nil {
		return nil, validateScopeErr
	}
	var state consolidated
	var err error
	if memoryService.c.Store != nil {
		state, err = memoryService.readSharedState(ctx, scope)
	} else {
		state, err = memoryService.readLocalState(scope)
	}
	if err != nil {
		return nil, err
	}
	return &agentmodel.MemorySnapshot{Scope: scope, Summary: state.Summary, UpdatedAt: state.UpdatedAt}, nil
}

func validateScope(ctx context.Context, scope string) error {
	err := ctx.Err()
	if err != nil {
		return err
	}
	if scope == "" || strings.TrimSpace(scope) != scope || strings.HasSuffix(scope, "/") || strings.ContainsRune(scope, 0) {
		return errors.New("memory requires a nonempty canonical scope")
	}
	return nil
}
