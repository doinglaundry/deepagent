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
	"eino-cli/backend/runtime"
	"eino-cli/backend/sandbox"
)

const (
	defaultMaxCachedThreads = 256
	genericID               = "local"
	threadIDPrefix          = "local:"
)

// Manager is the per-thread Sandbox factory plus LRU cache.
type Manager struct {
	cfg            *config.Config
	staticMappings []PathMapping

	mu              sync.Mutex
	generic         *Sandbox
	cache           map[string]*list.Element
	order           *list.List
	maxCachedThread int
}

type cacheEntry struct {
	tid     string
	sandbox *Sandbox
}

// New builds the local Manager from cfg.
func New(cfg *config.Config) (sandbox.SandboxManager, error) {
	m := &Manager{
		cfg:             cfg,
		staticMappings:  setupStaticMappings(cfg),
		cache:           map[string]*list.Element{},
		order:           list.New(),
		maxCachedThread: defaultMaxCachedThreads,
	}
	return m, nil
}

// Acquire returns "local" for empty tid, or "local:<tid>" with per-thread mappings.
func (m *Manager) Acquire(ctx context.Context, tid string) (string, error) {
	if tid == "" {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.generic == nil {
			m.generic = newSandbox(genericID, append([]PathMapping{}, m.staticMappings...))
		}
		return m.generic.id, nil
	}

	m.mu.Lock()
	if el, ok := m.cache[tid]; ok {
		m.order.MoveToFront(el)
		id := el.Value.(*cacheEntry).sandbox.id
		m.mu.Unlock()
		return id, nil
	}
	m.mu.Unlock()

	// Build outside the lock — touches the filesystem.
	uid := runtime.GetEffectiveUserID(ctx)
	threadMappings, err := buildThreadPathMappings(m.cfg, tid, uid)
	if err != nil {
		return "", err
	}
	all := append([]PathMapping{}, m.staticMappings...)
	all = append(all, threadMappings...)

	m.mu.Lock()
	defer m.mu.Unlock()
	if el, ok := m.cache[tid]; ok {
		m.order.MoveToFront(el)
		return el.Value.(*cacheEntry).sandbox.id, nil
	}
	sb := newSandbox(threadIDPrefix+tid, all)
	el := m.order.PushFront(&cacheEntry{tid: tid, sandbox: sb})
	m.cache[tid] = el
	m.evictLocked()
	return sb.id, nil
}

func (m *Manager) evictLocked() {
	for m.order.Len() > m.maxCachedThread {
		el := m.order.Back()
		if el == nil {
			return
		}
		entry := el.Value.(*cacheEntry)
		delete(m.cache, entry.tid)
		m.order.Remove(el)
	}
}

// Get returns the Sandbox by id, bumping its LRU position.
func (m *Manager) Get(ctx context.Context, sandboxID string) (sandbox.Sandbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sandboxID == genericID {
		if m.generic == nil {
			return nil, sandbox.NewNotFoundError(sandboxID)
		}
		return m.generic, nil
	}
	if tid, ok := strings.CutPrefix(sandboxID, threadIDPrefix); ok {
		if el, ok := m.cache[tid]; ok {
			m.order.MoveToFront(el)
			return el.Value.(*cacheEntry).sandbox, nil
		}
	}
	return nil, sandbox.NewNotFoundError(sandboxID)
}

// Release is a no-op; LRU eviction in Acquire is the only path that drops entries.
func (m *Manager) Release(ctx context.Context, sandboxID string) error { return nil }

// Reset clears every cached sandbox.
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generic = nil
	m.cache = map[string]*list.Element{}
	m.order = list.New()
}

// UsesThreadDataMounts reports true; local always reverse-maps /mnt paths.
func (m *Manager) UsesThreadDataMounts() bool { return true }

// setupStaticMappings derives skills + custom mounts (no per-thread paths).
func setupStaticMappings(cfg *config.Config) []PathMapping {
	var out []PathMapping
	out = append(out, skillsMapping(cfg)...)

	reserved := map[string]bool{
		"/mnt/user-data": true,
		"/mnt/skills":    true,
	}
	for _, mount := range cfg.Sandbox.Mounts {
		host := mount.HostPath
		container := strings.TrimRight(mount.ContainerPath, "/")
		if container == "" {
			container = "/"
		}
		if !filepath.IsAbs(host) || !strings.HasPrefix(container, "/") {
			continue
		}
		if reserved[container] {
			continue
		}
		if _, err := os.Stat(host); err != nil {
			continue
		}
		abs, err := filepath.Abs(host)
		if err != nil {
			continue
		}
		out = append(out, PathMapping{
			ContainerPath: container,
			LocalPath:     abs,
			ReadOnly:      mount.ReadOnly,
		})
	}
	return out
}

// skillsMapping returns the first existing cfg.Skills.Paths bound read-only at /mnt/skills.
func skillsMapping(cfg *config.Config) []PathMapping {
	for _, p := range cfg.Skills.Paths {
		if info, err := os.Stat(p); err == nil && info.IsDir() {
			abs, err := filepath.Abs(p)
			if err != nil {
				continue
			}
			return []PathMapping{{
				ContainerPath: "/mnt/skills",
				LocalPath:     abs,
				ReadOnly:      true,
			}}
		}
	}
	return nil
}

// buildThreadPathMappings builds the four /mnt/user-data/* mappings for (tid, uid).
func buildThreadPathMappings(cfg *config.Config, tid, uid string) ([]PathMapping, error) {
	if err := config.EnsureThreadDirs(cfg, tid, uid); err != nil {
		return nil, fmt.Errorf("local manager: ensure dirs: %w", err)
	}
	return []PathMapping{
		// Parent first so Glob/ListDir on /mnt/user-data has a real dir to walk.
		{ContainerPath: "/mnt/user-data", LocalPath: config.SandboxUserDataDir(cfg, tid, uid)},
		{ContainerPath: "/mnt/user-data/workspace", LocalPath: config.SandboxWorkDir(cfg, tid, uid)},
		{ContainerPath: "/mnt/user-data/uploads", LocalPath: config.SandboxUploadsDir(cfg, tid, uid)},
		{ContainerPath: "/mnt/user-data/outputs", LocalPath: config.SandboxOutputsDir(cfg, tid, uid)},
	}, nil
}

func init() {
	sandbox.RegisterLocalFactory(New)
}
