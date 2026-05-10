package agent

import (
	"context"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"eino-cli/backend/config"
	memorystore "eino-cli/backend/memory/store"
)

// memoryUpdateTimeout is the per-Run cap on the LLM call. memoryFlushTimeout
// is the shorter cap used by the /clear summarization flush so it cannot
// stall UI.
const (
	memoryUpdateTimeout = 60 * time.Second
	memoryFlushTimeout  = 5 * time.Second
)

// MemoryUpdater serialises LLM-driven memory updates per (store, agent).
// chatModel/cfg/agentName are passed to Run so the same updater can serve
// memory hooks and the summarization flush hook with their own contexts.
type MemoryUpdater struct {
	store *memorystore.Store

	mu        sync.Mutex
	lastRunAt time.Time
}

// NewMemoryUpdater returns an updater that writes to the given store. Pass
// the same store the reader uses; otherwise debounce/lastRunAt won't line up
// with what landed on disk.
func NewMemoryUpdater(store *memorystore.Store) *MemoryUpdater {
	return &MemoryUpdater{store: store}
}

// Run is wired into the middleware chain in commit 1 but is intentionally a
// no-op until commit 2 fills in the LLM call + merge. Keeping the stub here
// pins the public signature so call sites compile against the final shape.
func (u *MemoryUpdater) Run(
	ctx context.Context,
	chatModel model.BaseChatModel,
	cfg config.Memory,
	agentName string,
	messages []*schema.Message,
	force bool,
) error {
	_ = ctx
	_ = chatModel
	_ = cfg
	_ = agentName
	_ = messages
	_ = force
	return nil
}
