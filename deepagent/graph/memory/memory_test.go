package memory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	messagepkg "eino-cli/deepagent/message"
	memorypkg "eino-cli/deepagent/protocol/memory"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type memoryStore struct {
	mu        sync.Mutex
	leases    map[string]memorypkg.Lease
	artifacts map[string]memorypkg.Artifact
}

func newMemoryStore() *memoryStore {
	return &memoryStore{leases: map[string]memorypkg.Lease{}, artifacts: map[string]memorypkg.Artifact{}}
}

func (memoryStore *memoryStore) ClaimMemory(_ context.Context, key, _ string, ttl time.Duration) (memorypkg.Lease, error) {
	memoryStore.mu.Lock()
	defer memoryStore.mu.Unlock()
	existingLease, exists := memoryStore.leases[key]
	if exists && time.Now().Before(existingLease.ExpiresAt) {
		return memorypkg.Lease{}, memorypkg.ErrConflict
	}
	lease := memorypkg.Lease{Key: key, Token: uuid.NewString(), ExpiresAt: time.Now().Add(ttl)}
	memoryStore.leases[key] = lease
	return lease, nil
}

func (memoryStore *memoryStore) RenewMemory(_ context.Context, lease memorypkg.Lease, ttl time.Duration) (memorypkg.Lease, error) {
	memoryStore.mu.Lock()
	defer memoryStore.mu.Unlock()
	current, exists := memoryStore.leases[lease.Key]
	if !exists || current.Token != lease.Token || time.Now().After(current.ExpiresAt) {
		return memorypkg.Lease{}, memorypkg.ErrLeaseLost
	}
	lease.ExpiresAt = time.Now().Add(ttl)
	memoryStore.leases[lease.Key] = lease
	return lease, nil
}

func (memoryStore *memoryStore) CompleteMemory(_ context.Context, lease memorypkg.Lease, version string, data []byte) error {
	memoryStore.mu.Lock()
	defer memoryStore.mu.Unlock()
	current, exists := memoryStore.leases[lease.Key]
	if !exists || current.Token != lease.Token || time.Now().After(current.ExpiresAt) {
		return memorypkg.ErrLeaseLost
	}
	memoryStore.artifacts[lease.Key] = memorypkg.Artifact{Version: version, Data: append([]byte(nil), data...)}
	return nil
}

func (memoryStore *memoryStore) ReleaseMemory(_ context.Context, lease memorypkg.Lease) error {
	memoryStore.mu.Lock()
	defer memoryStore.mu.Unlock()
	current, exists := memoryStore.leases[lease.Key]
	if !exists || current.Token != lease.Token {
		return memorypkg.ErrLeaseLost
	}
	delete(memoryStore.leases, lease.Key)
	return nil
}

func (memoryStore *memoryStore) GetMemory(_ context.Context, key string) (memorypkg.Artifact, error) {
	memoryStore.mu.Lock()
	defer memoryStore.mu.Unlock()
	artifact, exists := memoryStore.artifacts[key]
	if !exists {
		return memorypkg.Artifact{}, memorypkg.ErrNotFound
	}
	return artifact, nil
}

func (memoryStore *memoryStore) ListMemory(_ context.Context, prefix string, limit, offset int) (map[string]memorypkg.Artifact, error) {
	memoryStore.mu.Lock()
	defer memoryStore.mu.Unlock()
	result := map[string]memorypkg.Artifact{}
	for key, artifact := range memoryStore.artifacts {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if offset > 0 {
			offset--
			continue
		}
		if limit > 0 && len(result) >= limit {
			break
		}
		result[key] = artifact
	}
	return result, nil
}

var _ memorypkg.Store = (*memoryStore)(nil)

type memoryModel struct{ calls int }

func (chatModel *memoryModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return chatModel, nil
}
func (chatModel *memoryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("memory extraction bypassed Graph")
}
func (chatModel *memoryModel) Stream(_ context.Context, messages []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	chatModel.calls++
	if chatModel.calls == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("User prefers Go. Project uses MySQL.", nil)}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("# Memory\n- User prefers Go.\n- Project uses MySQL.", nil)}), nil
}
func TestExtractionConsolidationAndRestartBaseline(t *testing.T) {
	ctx := context.Background()
	chatModel := &memoryModel{}
	root := t.TempDir()
	memoryService, err := New(Config{Root: root, Model: chatModel, Consolidator: func(context.Context, string, string) (string, error) {
		chatModel.calls++
		return "# Memory\n- User prefers Go.\n- Project uses MySQL.", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	messages := []*messagepkg.Message{messagepkg.NewUserMessage("Use Go and MySQL.")}
	err = memoryService.Observe(ctx, "local", "session/thread", messages)
	if err != nil {
		t.Fatal(err)
	}
	err = memoryService.Observe(ctx, "local", "session/thread", messages)
	if err != nil {
		t.Fatal(err)
	}
	if chatModel.calls != 1 {
		t.Fatal("unchanged source extracted twice")
	}
	err = memoryService.Consolidate(ctx, "local")
	if err != nil {
		t.Fatal(err)
	}
	secondMemoryService, err := New(Config{Root: root, Model: chatModel, Consolidator: func(context.Context, string, string) (string, error) {
		chatModel.calls++
		return "# Memory\n- User prefers Go.\n- Project uses MySQL.", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	err = secondMemoryService.Consolidate(ctx, "local")
	if err != nil {
		t.Fatal(err)
	}
	if chatModel.calls != 2 {
		t.Fatal("unchanged extractions consolidated twice")
	}
	summary, err := secondMemoryService.Read(ctx, "local")
	if err != nil || summary.Summary != "# Memory\n- User prefers Go.\n- Project uses MySQL." {
		t.Fatal(summary, err)
	}
}

func TestDurableArtifactsResumeOnDifferentWorkerDirectory(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	chatModel := &memoryModel{}
	consolidate := func(context.Context, string, string) (string, error) { return "Shared durable memory", nil }
	memoryService, err := New(Config{Root: t.TempDir(), Model: chatModel, Consolidator: consolidate, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	err = memoryService.Observe(ctx, "user/u1", "thread1", []*messagepkg.Message{messagepkg.NewUserMessage("Go preference")})
	if err != nil {
		t.Fatal(err)
	}
	secondMemoryService, err := New(Config{Root: t.TempDir(), Model: chatModel, Consolidator: consolidate, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	err = secondMemoryService.Consolidate(ctx, "user/u1")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := memoryService.Read(ctx, "user/u1")
	if err != nil || snapshot.Summary != "Shared durable memory" {
		t.Fatal(snapshot, err)
	}
	otherMemoryService, _ := New(Config{Root: t.TempDir(), Model: chatModel, Consolidator: consolidate, Store: store})
	snapshot, err = otherMemoryService.Read(ctx, "user/other")
	if err != nil || snapshot.Summary != "" {
		t.Fatal("memory user scope leaked", snapshot, err)
	}
}
