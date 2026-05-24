package local

import (
	"container/list"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"eino-cli/backend/config"
	"eino-cli/backend/consts"
	"eino-cli/backend/runtime"
	"eino-cli/backend/sandbox"
)

type SandboxManager struct {
	mu                sync.Mutex
	generic           *Sandbox
	entriesByThreadId map[string]*list.Element
	order             *list.List
	maxCachedThread   int
}

type cacheEntry struct {
	tid     string
	sandbox *Sandbox
}

// New builds the local SandboxManager from cfg.
func New() (sandbox.SandboxManager, error) {
	return &SandboxManager{
		generic:           newSandbox(consts.GenericLocalSandboxID, GetSkillPathMappings()),
		entriesByThreadId: map[string]*list.Element{},
		order:             list.New(),
		maxCachedThread:   consts.DefaultMaxCachedThreads,
	}, nil
}

// Acquire returns "local" for empty tid, or "local:<tid>" with per-thread mappings.
func (m *SandboxManager) Acquire(ctx context.Context, tid string) (string, error) {
	if tid == "" {
		return m.generic.id, nil
	}

	if sb := m.getCachedLocked(tid); sb != nil {
		return sb.id, nil
	}

	// Build outside the lock — touches the filesystem.
	uid := runtime.GetEffectiveUserID(ctx)
	userDataPathMappings, err := getUserDataPathMappings(tid, uid)
	if err != nil {
		return "", err
	}
	all := make([]PathMapping, 0)
	all = append(all, GetSkillPathMappings()...)
	all = append(all, userDataPathMappings...)

	sb := newSandbox(consts.LocalThreadIDPrefix+tid, all)
	m.putCachedLocked(tid, sb)
	return sb.id, nil
}

func (m *SandboxManager) getCachedLocked(tid string) *Sandbox {
	m.mu.Lock()
	defer m.mu.Unlock()

	elem, ok := m.entriesByThreadId[tid]
	if !ok {
		return nil
	}
	m.order.MoveToFront(elem)
	return elem.Value.(*cacheEntry).sandbox
}

func (m *SandboxManager) putCachedLocked(tid string, sb *Sandbox) {
	m.entriesByThreadId[tid] = m.order.PushFront(&cacheEntry{tid: tid, sandbox: sb})
	m.evictLocked()
}

func (m *SandboxManager) evictLocked() {
	for m.order.Len() > m.maxCachedThread {
		el := m.order.Back()
		if el == nil {
			return
		}
		entry := el.Value.(*cacheEntry)
		delete(m.entriesByThreadId, entry.tid)
		m.order.Remove(el)
	}
}

func (m *SandboxManager) Get(ctx context.Context, sandboxID string) (sandbox.Sandbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sandboxID == consts.GenericLocalSandboxID && m.generic != nil {
		return m.generic, nil
	}

	tid, ok := strings.CutPrefix(sandboxID, consts.LocalThreadIDPrefix)
	if ok {
		elem, ok := m.entriesByThreadId[tid]
		if ok {
			m.order.MoveToFront(elem)
			return elem.Value.(*cacheEntry).sandbox, nil
		}
	}

	return nil, sandbox.NewNotFoundError(sandboxID)
}

func (m *SandboxManager) Release(ctx context.Context, sandboxID string) error { return nil }

func (m *SandboxManager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generic = nil
	m.entriesByThreadId = map[string]*list.Element{}
	m.order = list.New()
}

func (m *SandboxManager) UsesThreadDataMounts() bool { return true }

func (m *SandboxManager) AllowsIsolatedExec() bool { return false }

func GetSkillPathMappings() (res []PathMapping) {
	res = make([]PathMapping, 0)
	p := filepath.Join(config.RootDir(), "backend", "skills")
	info, err := os.Stat(p)
	if err != nil || !info.IsDir() {
		return nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return
	}
	res = append(res, PathMapping{
		ContainerPath: "/mnt/skills",
		LocalPath:     abs,
		ReadOnly:      true,
	})
	return
}

func getUserDataPathMappings(tid, uid string) ([]PathMapping, error) {
	if err := config.EnsureThreadDirs(tid, uid); err != nil {
		return nil, fmt.Errorf("local manager: ensure dirs: %w", err)
	}
	return []PathMapping{
		{ContainerPath: "/mnt/user-data", LocalPath: config.SandboxUserDataDir(tid, uid)},
		{ContainerPath: "/mnt/user-data/workspace", LocalPath: config.SandboxWorkDir(tid, uid)},
		{ContainerPath: "/mnt/user-data/uploads", LocalPath: config.SandboxUploadsDir(tid, uid)},
		{ContainerPath: "/mnt/user-data/outputs", LocalPath: config.SandboxOutputsDir(tid, uid)},
	}, nil
}
