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
	// defaultMaxCachedThreads bounds the per-thread Sandbox cache. Each
	// entry is cheap (struct + slice + small map) but a long-running
	// gateway sees an unbounded thread_id stream.
	defaultMaxCachedThreads = 256

	// genericID is what we return when acquire(nil-tid). Kept for parity
	// with deer-flow's "local" singleton used by CLI / tests.
	genericID = "local"

	threadIDPrefix = "local:"
)

// Manager: per-thread sandbox factory + LRU cache + shared static mappings.
type Manager struct {
	cfg            *config.Config
	staticMappings []PathMapping

	mu              sync.Mutex
	generic         *Sandbox
	cache           map[string]*list.Element // tid -> *list.Element holding *cacheEntry
	order           *list.List
	maxCachedThread int
}

type cacheEntry struct {
	tid     string
	sandbox *Sandbox
}

// New builds a Manager from cfg. Reads cfg.Sandbox.Mounts + cfg.Skills to
// derive the static (non-thread) mappings once at startup.
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

// Acquire: empty tid → generic singleton (id="local"); non-empty tid →
// per-thread sandbox (id="local:<tid>") with mappings that resolve
// /mnt/user-data/{workspace,uploads,outputs} to that thread's host dir.
func (m *Manager) Acquire(ctx context.Context, tid string) (string, error) {
	if tid == "" {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.generic == nil {
			m.generic = newSandbox(genericID, append([]PathMapping{}, m.staticMappings...))
		}
		return m.generic.id, nil
	}

	// Fast path: cache hit.
	m.mu.Lock()
	if el, ok := m.cache[tid]; ok {
		m.order.MoveToFront(el)
		id := el.Value.(*cacheEntry).sandbox.id
		m.mu.Unlock()
		return id, nil
	}
	m.mu.Unlock()

	// Slow path: build mappings outside the lock (touches the filesystem).
	uid := runtime.GetEffectiveUserID(ctx)
	threadMappings, err := buildThreadPathMappings(m.cfg, tid, uid)
	if err != nil {
		return "", err
	}
	all := append([]PathMapping{}, m.staticMappings...)
	all = append(all, threadMappings...)

	m.mu.Lock()
	defer m.mu.Unlock()
	// Re-check after the unlocked I/O.
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

// Get looks up a previously-acquired sandbox. Bumps LRU position so a
// thread that's actively tooling doesn't age out under load.
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

// Release is a no-op for LocalSandbox — keep the cached instance so
// agent-written-paths survives between turns. LRU eviction in Acquire is
// the only path that drops entries.
func (m *Manager) Release(ctx context.Context, sandboxID string) error { return nil }

// Reset clears every cached sandbox (config change, test teardown).
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generic = nil
	m.cache = map[string]*list.Element{}
	m.order = list.New()
}

// UsesThreadDataMounts: tools check this before reverse-mapping /mnt paths.
// LocalSandbox always does, so it's a static true.
func (m *Manager) UsesThreadDataMounts() bool { return true }

// setupStaticMappings: skills (read-only) + cfg.Sandbox.Mounts custom
// bindings. Reserved virtual prefixes are not added here — those come
// from buildThreadPathMappings per-tid.
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

// skillsMapping picks the first existing dir from cfg.Skills.Paths and
// publishes it read-only at /mnt/skills. Multi-skills-dir users get
// whichever path wins by config order — same behaviour as Python.
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

// buildThreadPathMappings produces the four /mnt/user-data/* mappings for
// (tid, uid) and runs EnsureThreadDirs so the host dirs exist by the time
// the LLM reads from them.
func buildThreadPathMappings(cfg *config.Config, tid, uid string) ([]PathMapping, error) {
	if err := config.EnsureThreadDirs(cfg, tid, uid); err != nil {
		return nil, fmt.Errorf("local manager: ensure dirs: %w", err)
	}
	return []PathMapping{
		// Parent first — Glob / ListDir on /mnt/user-data needs a real
		// directory to walk. Subpath mappings below win for nested ops.
		{ContainerPath: "/mnt/user-data", LocalPath: config.SandboxUserDataDir(cfg, tid, uid)},
		{ContainerPath: "/mnt/user-data/workspace", LocalPath: config.SandboxWorkDir(cfg, tid, uid)},
		{ContainerPath: "/mnt/user-data/uploads", LocalPath: config.SandboxUploadsDir(cfg, tid, uid)},
		{ContainerPath: "/mnt/user-data/outputs", LocalPath: config.SandboxOutputsDir(cfg, tid, uid)},
	}, nil
}

// init wires the factory so sandbox.NewSandboxManager finds us via the
// registry. No global mutable state outside this one call.
func init() {
	sandbox.RegisterLocalFactory(New)
}
