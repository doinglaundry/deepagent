package memory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	memorypkg "eino-cli/deepagent/protocol/memory"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type Config struct {
	Store        memorypkg.Store
	LeaseTTL     time.Duration
	Root         string
	Model        model.ToolCallingChatModel
	Consolidator func(context.Context, string, string) (string, error)
}

type Service interface {
	Read(ctx context.Context, scope string) (*Snapshot, error)
	Observe(ctx context.Context, scope, threadID string, messages []*schema.Message) error
	Consolidate(ctx context.Context, scope string) error
}

type Snapshot struct {
	Scope     string
	Summary   string
	UpdatedAt time.Time
}

type memoryService struct{ c Config }

func New(c Config) (Service, error) {
	if c.Model == nil || c.Root == "" {
		return nil, errors.New("memory requires model and root")
	}
	if c.LeaseTTL <= 0 {
		c.LeaseTTL = 30 * time.Second
	}
	if c.LeaseTTL < 3*time.Millisecond {
		return nil, errors.New("memory lease TTL must be at least 3ms")
	}
	root, e := filepath.Abs(c.Root)
	if e != nil {
		return nil, e
	}
	c.Root = root
	e = os.MkdirAll(root, 0700)
	if e != nil {
		return nil, e
	}
	if c.Consolidator == nil {
		c.Consolidator = AgentConsolidator(c.Model, c.Root)
	}
	return &memoryService{c}, nil
}

func (p *memoryService) Read(ctx context.Context, scope string) (*Snapshot, error) {
	validateScopeErr := validateScope(ctx, scope)
	if validateScopeErr != nil {
		return nil, validateScopeErr
	}
	var state consolidated
	var err error
	if p.c.Store != nil {
		state, err = p.sharedState(ctx, scope)
	} else {
		state, err = p.state(scope)
	}
	if err != nil {
		return nil, err
	}
	return &Snapshot{Scope: scope, Summary: state.Summary, UpdatedAt: state.UpdatedAt}, nil
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
