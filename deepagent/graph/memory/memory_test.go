package memory

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

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

func (s *memoryStore) ClaimMemory(_ context.Context, key, _ string, ttl time.Duration) (memorypkg.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	leasesLease, ok := s.leases[key]
	if ok && time.Now().Before(leasesLease.ExpiresAt) {
		return memorypkg.Lease{}, memorypkg.ErrConflict
	}
	lease := memorypkg.Lease{Key: key, Token: uuid.NewString(), ExpiresAt: time.Now().Add(ttl)}
	s.leases[key] = lease
	return lease, nil
}

func (s *memoryStore) RenewMemory(_ context.Context, lease memorypkg.Lease, ttl time.Duration) (memorypkg.Lease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.leases[lease.Key]
	if !ok || current.Token != lease.Token || time.Now().After(current.ExpiresAt) {
		return memorypkg.Lease{}, memorypkg.ErrLeaseLost
	}
	lease.ExpiresAt = time.Now().Add(ttl)
	s.leases[lease.Key] = lease
	return lease, nil
}

func (s *memoryStore) CompleteMemory(_ context.Context, lease memorypkg.Lease, version string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.leases[lease.Key]
	if !ok || current.Token != lease.Token || time.Now().After(current.ExpiresAt) {
		return memorypkg.ErrLeaseLost
	}
	s.artifacts[lease.Key] = memorypkg.Artifact{Version: version, Data: append([]byte(nil), data...)}
	return nil
}

func (s *memoryStore) ReleaseMemory(_ context.Context, lease memorypkg.Lease) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.leases[lease.Key]
	if !ok || current.Token != lease.Token {
		return memorypkg.ErrLeaseLost
	}
	delete(s.leases, lease.Key)
	return nil
}

func (s *memoryStore) GetMemory(_ context.Context, key string) (memorypkg.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	artifact, ok := s.artifacts[key]
	if !ok {
		return memorypkg.Artifact{}, memorypkg.ErrNotFound
	}
	return artifact, nil
}

func (s *memoryStore) ListMemory(_ context.Context, prefix string, limit, offset int) (map[string]memorypkg.Artifact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := map[string]memorypkg.Artifact{}
	for key, artifact := range s.artifacts {
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

func (m *memoryModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}
func (m *memoryModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return nil, errors.New("memory extraction bypassed Graph")
}
func (m *memoryModel) Stream(_ context.Context, in []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.calls++
	if m.calls == 1 {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("User prefers Go. Project uses MySQL.", nil)}), nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("# Memory\n- User prefers Go.\n- Project uses MySQL.", nil)}), nil
}
func TestExtractionConsolidationAndRestartBaseline(t *testing.T) {
	ctx := context.Background()
	m := &memoryModel{}
	root := t.TempDir()
	p, e := New(Config{Root: root, Model: m, Consolidator: func(context.Context, string, string) (string, error) {
		m.calls++
		return "# Memory\n- User prefers Go.\n- Project uses MySQL.", nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	messages := []*schema.Message{schema.UserMessage("Use Go and MySQL.")}
	e = p.Observe(ctx, "local", "session/thread", messages)
	if e != nil {
		t.Fatal(e)
	}
	e = p.Observe(ctx, "local", "session/thread", messages)
	if e != nil {
		t.Fatal(e)
	}
	if m.calls != 1 {
		t.Fatal("unchanged source extracted twice")
	}
	e = p.Consolidate(ctx, "local")
	if e != nil {
		t.Fatal(e)
	}
	p2, e := New(Config{Root: root, Model: m, Consolidator: func(context.Context, string, string) (string, error) {
		m.calls++
		return "# Memory\n- User prefers Go.\n- Project uses MySQL.", nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	e = p2.Consolidate(ctx, "local")
	if e != nil {
		t.Fatal(e)
	}
	if m.calls != 2 {
		t.Fatal("unchanged extractions consolidated twice")
	}
	summary, e := p2.Read(ctx, "local")
	if e != nil || summary.Summary != "# Memory\n- User prefers Go.\n- Project uses MySQL." {
		t.Fatal(summary, e)
	}
}

func TestDurableArtifactsResumeOnDifferentWorkerDirectory(t *testing.T) {
	ctx := context.Background()
	store := newMemoryStore()
	m := &memoryModel{}
	consolidate := func(context.Context, string, string) (string, error) { return "Shared durable memory", nil }
	p, e := New(Config{Root: t.TempDir(), Model: m, Consolidator: consolidate, Store: store})
	if e != nil {
		t.Fatal(e)
	}
	e = p.Observe(ctx, "user/u1", "thread1", []*schema.Message{schema.UserMessage("Go preference")})
	if e != nil {
		t.Fatal(e)
	}
	p2, e := New(Config{Root: t.TempDir(), Model: m, Consolidator: consolidate, Store: store})
	if e != nil {
		t.Fatal(e)
	}
	e = p2.Consolidate(ctx, "user/u1")
	if e != nil {
		t.Fatal(e)
	}
	out, e := p.Read(ctx, "user/u1")
	if e != nil || out.Summary != "Shared durable memory" {
		t.Fatal(out, e)
	}
	p3, _ := New(Config{Root: t.TempDir(), Model: m, Consolidator: consolidate, Store: store})
	out, e = p3.Read(ctx, "user/other")
	if e != nil || out.Summary != "" {
		t.Fatal("memory user scope leaked", out, e)
	}
}
