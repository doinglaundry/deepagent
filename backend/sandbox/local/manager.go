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

type SandboxManagerLocal struct {
	mu                  sync.Mutex
	defaultSandbox      *Sandbox
	entriesBySessionID  map[string]*list.Element
	order               *list.List
	maxCachedSessions   int
}

type cacheEntry struct {
	sessionID string
	sandbox   *Sandbox
}

// New builds the local SandboxManagerLocal from cfg.
func New() (sandbox.SandboxManager, error) {

	return &SandboxManagerLocal{
		defaultSandbox:     newSandbox(consts.GenericLocalSandboxID),
		entriesBySessionID: map[string]*list.Element{},
		order:              list.New(),
		maxCachedSessions:  consts.DefaultMaxCachedSessions,
	}, nil
}

// Acquire returns "local" for empty sessionID, or "local:<sessionID>" with per-session mappings.
func (m *SandboxManagerLocal) Acquire(ctx context.Context, sessionID string) (string, error) {
	if sessionID == "" {
		return m.defaultSandbox.id, nil
	}
	if sb := m.getCachedLocked(sessionID); sb != nil {
		return sb.id, nil
	}
	uid := runtime.GetEffectiveUserID(ctx)
	userDataPathMappings, err := getUserDataPathMappings(sessionID, uid)
	if err != nil {
		return "", err
	}
	sb := newSandbox(consts.LocalSessionIDPrefix + sessionID)
	sb.AppendPathMappings(userDataPathMappings)
	m.putCachedLocked(sessionID, sb)
	return sb.id, nil
}

func (m *SandboxManagerLocal) getCachedLocked(sessionID string) *Sandbox {
	m.mu.Lock()
	defer m.mu.Unlock()

	elem, ok := m.entriesBySessionID[sessionID]
	if !ok {
		return nil
	}
	m.order.MoveToFront(elem)
	return elem.Value.(*cacheEntry).sandbox
}

func (m *SandboxManagerLocal) putCachedLocked(sessionID string, sb *Sandbox) {
	m.entriesBySessionID[sessionID] = m.order.PushFront(&cacheEntry{sessionID: sessionID, sandbox: sb})
	m.evictLocked()
}

func (m *SandboxManagerLocal) evictLocked() {
	for m.order.Len() > m.maxCachedSessions {
		el := m.order.Back()
		if el == nil {
			return
		}
		entry := el.Value.(*cacheEntry)
		delete(m.entriesBySessionID, entry.sessionID)
		m.order.Remove(el)
	}
}

func (m *SandboxManagerLocal) Get(ctx context.Context, sandboxID string) (sandbox.Sandbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sandboxID == consts.GenericLocalSandboxID && m.defaultSandbox != nil {
		return m.defaultSandbox, nil
	}

	sessionID, ok := strings.CutPrefix(sandboxID, consts.LocalSessionIDPrefix)
	if ok {
		elem, ok := m.entriesBySessionID[sessionID]
		if ok {
			m.order.MoveToFront(elem)
			return elem.Value.(*cacheEntry).sandbox, nil
		}
	}

	return nil, sandbox.NewNotFoundError(sandboxID)
}

func (m *SandboxManagerLocal) Release(ctx context.Context, sandboxID string) error { return nil }

func (m *SandboxManagerLocal) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.defaultSandbox = nil
	m.entriesBySessionID = map[string]*list.Element{}
	m.order = list.New()
}

func (m *SandboxManagerLocal) UsesSessionDataMounts() bool { return true }

func (m *SandboxManagerLocal) AllowsIsolatedExec() bool { return false }

func GetSkillPathMappings() (res []*PathMapping) {
	res = make([]*PathMapping, 0)
	skillPath := filepath.Join(config.RootDir(), "backend", "skills")
	info, err := os.Stat(skillPath)
	if err != nil || !info.IsDir() {
		return nil
	}
	absSkillPath, err := filepath.Abs(skillPath)
	if err != nil {
		return
	}
	res = append(res, &PathMapping{
		ContainerPath: "/mnt/skills",
		LocalPath:     absSkillPath,
		ReadOnly:      true,
	})
	return
}

func getUserDataPathMappings(sessionID, uid string) ([]*PathMapping, error) {
	if err := config.EnsureSessionDirs(sessionID, uid); err != nil {
		return nil, fmt.Errorf("local manager: ensure dirs: %w", err)
	}
	return []*PathMapping{
		{ContainerPath: "/mnt/user-data", LocalPath: config.SandboxUserDataDir(sessionID, uid)},
		{ContainerPath: "/mnt/user-data/workspace", LocalPath: config.SandboxWorkDir(sessionID, uid)},
		{ContainerPath: "/mnt/user-data/uploads", LocalPath: config.SandboxUploadsDir(sessionID, uid)},
		{ContainerPath: "/mnt/user-data/outputs", LocalPath: config.SandboxOutputsDir(sessionID, uid)},
	}, nil
}
