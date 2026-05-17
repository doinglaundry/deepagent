package aio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"golang.org/x/sys/unix"

	"eino-cli/backend/config"
	"eino-cli/backend/runtime"
	"eino-cli/backend/sandbox"
	"eino-cli/backend/util/network"
)

// Manager owns the lifecycle of every aio sandbox in this process plus the
// cross-process flock that prevents two CLIs racing to create the same
// container. State maps are guarded by mu; per-thread singleflight makes
// Acquire idempotent under concurrent first-time callers.
type Manager struct {
	cfg *config.Config // global cfg (sandbox subsection + per-thread paths)
	rt  containerRuntime
	log *slog.Logger

	mu               sync.Mutex
	sandboxes        map[string]*Sandbox     // sid -> live HTTP client
	infos            map[string]SandboxInfo  // sid -> container record
	threadSandboxes  map[string]string       // tid -> sid
	lastActivity     map[string]time.Time    // sid -> last touch
	warmPool         map[string]warmEntry    // sid -> released-but-running

	sf       singleflight.Group
	stopIdle chan struct{}
	shutdown sync.Once
}

// New: factory wired from sandbox/factory.go via init(). Discovers any
// orphan containers up-front so they seed the warm pool.
func New(cfg *config.Config) (sandbox.SandboxManager, error) {
	m := &Manager{
		cfg:             cfg,
		rt:              detectRuntime(),
		log:             slog.Default(),
		sandboxes:       map[string]*Sandbox{},
		infos:           map[string]SandboxInfo{},
		threadSandboxes: map[string]string{},
		lastActivity:    map[string]time.Time{},
		warmPool:        map[string]warmEntry{},
		stopIdle:        make(chan struct{}),
	}
	if m.rt == "" {
		return nil, fmt.Errorf("aio: no container runtime (docker / container CLI)")
	}
	m.reconcileOrphans()
	if cfg.Sandbox.IdleTimeout > 0 {
		go m.idleLoop()
	}
	return m, nil
}

// sandboxCfg returns the sandbox-specific subsection of m.cfg. Tiny
// shim so the rest of the file reads `m.sandboxCfg().X` instead of
// `m.cfg.Sandbox.X` everywhere — that's the level the methods care about.
func (m *Manager) sandboxCfg() *config.SandboxConfig { return &m.cfg.Sandbox }

// deriveSandboxID is what makes cross-process discovery work: every
// process hashes the same thread_id to the same 8-hex-char id, so the
// container name (prefix + "-" + sid) collides on purpose.
func deriveSandboxID(tid string) string {
	sum := sha256.Sum256([]byte(tid))
	return hex.EncodeToString(sum[:])[:8]
}

// Acquire serializes per-tid via singleflight so a tool burst that all
// "first-touch" the same thread doesn't fire N container starts.
func (m *Manager) Acquire(ctx context.Context, tid string) (string, error) {
	if tid == "" {
		return "", sandbox.ErrThreadIDRequired
	}
	v, err, _ := m.sf.Do(tid, func() (any, error) {
		sid := deriveSandboxID(tid)
		if cached, ok := m.reuse(tid, sid); ok {
			return cached, nil
		}
		return m.discoverOrCreate(ctx, tid, sid)
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// reuse: in-process cache check + warm-pool revive, both under one lock.
// Returns the sid + true when we already know about this thread / there's
// a warm container waiting; false means cold-start path.
func (m *Manager) reuse(tid, sid string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cached, ok := m.threadSandboxes[tid]; ok {
		if _, alive := m.sandboxes[cached]; alive {
			m.lastActivity[cached] = time.Now()
			return cached, true
		}
		delete(m.threadSandboxes, tid)
	}
	entry, ok := m.warmPool[sid]
	if !ok {
		return "", false
	}
	delete(m.warmPool, sid)
	m.attachLocked(tid, sid, entry.info)
	return sid, true
}

// attachLocked centralises map mutations so call sites (warm revive,
// orphan adopt, fresh create) don't drift on which maps to touch.
// Caller must hold m.mu.
func (m *Manager) attachLocked(tid, sid string, info SandboxInfo) {
	m.sandboxes[sid] = newSandbox(sid, info.SandboxURL)
	m.infos[sid] = info
	if tid != "" {
		m.threadSandboxes[tid] = sid
	}
	m.lastActivity[sid] = time.Now()
}

// discoverOrCreate is the cold-start path: cross-process flock blocks
// sibling CLIs from starting the same container twice, then we either
// adopt a peer-started container or run one ourselves.
func (m *Manager) discoverOrCreate(ctx context.Context, tid, sid string) (string, error) {
	lockPath := filepath.Join(os.TempDir(), "eino-sandbox-"+sid+".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		return "", err
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()

	if info, ok := discoverContainer(m.rt, m.cfg.Sandbox.ContainerPrefix, sid); ok {
		m.mu.Lock()
		m.attachLocked(tid, sid, info)
		m.mu.Unlock()
		return sid, nil
	}
	return m.createSandbox(ctx, tid, sid)
}

func (m *Manager) createSandbox(ctx context.Context, tid, sid string) (string, error) {
	m.mu.Lock()
	m.evictUntilWithinReplicasLocked()
	m.mu.Unlock()

	port, err := network.GetFreePort(8081)
	if err != nil {
		return "", err
	}
	name := m.cfg.Sandbox.ContainerPrefix + "-" + sid
	cid, err := startContainer(ctx, containerSpec{
		Runtime: m.rt,
		Image:   m.cfg.Sandbox.Image,
		Name:    name,
		Port:    port,
		Mounts:  m.buildMounts(ctx, tid),
		Env:     m.cfg.Sandbox.Environment,
	})
	if err != nil {
		return "", err
	}
	info := SandboxInfo{
		SandboxID:     sid,
		SandboxURL:    fmt.Sprintf("http://localhost:%d", port),
		ContainerName: name,
		ContainerID:   cid,
		CreatedAt:     time.Now(),
	}
	readyCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := waitReady(readyCtx, info.SandboxURL); err != nil {
		_ = stopContainer(m.rt, cid)
		return "", fmt.Errorf("sandbox %s not ready: %w", sid, err)
	}
	m.mu.Lock()
	m.attachLocked(tid, sid, info)
	m.mu.Unlock()
	return sid, nil
}

// Get: look up by sid. Cheap — no I/O.
func (m *Manager) Get(ctx context.Context, sid string) (sandbox.Sandbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sandboxes[sid]
	if !ok {
		return nil, sandbox.NewNotFoundError(sid)
	}
	m.lastActivity[sid] = time.Now()
	return s, nil
}

// Release returns the sandbox to the warm pool — the container stays up
// so a quick re-acquire skips the cold start.
func (m *Manager) Release(ctx context.Context, sid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	info, ok := m.infos[sid]
	if !ok {
		return nil
	}
	delete(m.sandboxes, sid)
	delete(m.infos, sid)
	delete(m.lastActivity, sid)
	for tid, mapped := range m.threadSandboxes {
		if mapped == sid {
			delete(m.threadSandboxes, tid)
		}
	}
	m.warmPool[sid] = warmEntry{info: info, releasedAt: time.Now()}
	return nil
}

func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sandboxes = map[string]*Sandbox{}
	m.infos = map[string]SandboxInfo{}
	m.threadSandboxes = map[string]string{}
	m.lastActivity = map[string]time.Time{}
	m.warmPool = map[string]warmEntry{}
}

// UsesThreadDataMounts: aio bind-mounts per-thread dirs at container
// start, so the answer is yes — tools doing reverse path resolution
// should look at the thread's host dirs.
func (m *Manager) UsesThreadDataMounts() bool { return true }

// Shutdown is idempotent; tears down every container we know about
// (active + warm pool) on app exit.
func (m *Manager) Shutdown() {
	m.shutdown.Do(func() {
		close(m.stopIdle)
		m.mu.Lock()
		defer m.mu.Unlock()
		for sid, info := range m.infos {
			_ = stopContainer(m.rt, info.ContainerID)
			delete(m.infos, sid)
		}
		for sid, entry := range m.warmPool {
			_ = stopContainer(m.rt, entry.info.ContainerID)
			delete(m.warmPool, sid)
		}
	})
}

// idleLoop runs on a fixed cadence; the channel-based stop signal lets
// Shutdown deterministically tear it down without dangling goroutines.
func (m *Manager) idleLoop() {
	t := time.NewTicker(idleCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-m.stopIdle:
			return
		case <-t.C:
			m.cleanupIdle()
		}
	}
}

// cleanupIdle moves active sandboxes past their idle TTL into the warm
// pool, and tears down warm sandboxes that have been idle for the same
// budget on top. Single lock pass, two predicate iterations.
func (m *Manager) cleanupIdle() {
	if m.cfg.Sandbox.IdleTimeout <= 0 {
		return
	}
	cutoff := time.Now().Add(-m.cfg.Sandbox.IdleTimeout)
	m.mu.Lock()
	defer m.mu.Unlock()
	for sid, last := range m.lastActivity {
		if last.Before(cutoff) {
			info, ok := m.infos[sid]
			if !ok {
				continue
			}
			delete(m.sandboxes, sid)
			delete(m.infos, sid)
			delete(m.lastActivity, sid)
			for tid, mapped := range m.threadSandboxes {
				if mapped == sid {
					delete(m.threadSandboxes, tid)
				}
			}
			m.warmPool[sid] = warmEntry{info: info, releasedAt: time.Now()}
		}
	}
	for sid, entry := range m.warmPool {
		if entry.releasedAt.Before(cutoff) {
			_ = stopContainer(m.rt, entry.info.ContainerID)
			delete(m.warmPool, sid)
		}
	}
}

// evictUntilWithinReplicasLocked enforces cfg.Replicas: drop the oldest
// warm-pool entry first (cheapest — it's already idle), then the oldest
// active sandbox if even that isn't enough. Caller must hold m.mu.
func (m *Manager) evictUntilWithinReplicasLocked() {
	replicas := m.cfg.Sandbox.Replicas
	if replicas <= 0 {
		return
	}
	for len(m.sandboxes)+len(m.warmPool) >= replicas {
		if !m.evictOldestWarmLocked() {
			if !m.evictOldestActiveLocked() {
				return
			}
		}
	}
}

func (m *Manager) evictOldestWarmLocked() bool {
	if len(m.warmPool) == 0 {
		return false
	}
	var oldestSid string
	var oldestAt time.Time
	for sid, entry := range m.warmPool {
		if oldestSid == "" || entry.releasedAt.Before(oldestAt) {
			oldestSid = sid
			oldestAt = entry.releasedAt
		}
	}
	_ = stopContainer(m.rt, m.warmPool[oldestSid].info.ContainerID)
	delete(m.warmPool, oldestSid)
	return true
}

func (m *Manager) evictOldestActiveLocked() bool {
	if len(m.sandboxes) == 0 {
		return false
	}
	type entry struct {
		sid  string
		last time.Time
	}
	all := make([]entry, 0, len(m.lastActivity))
	for sid, last := range m.lastActivity {
		all = append(all, entry{sid, last})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].last.Before(all[j].last) })
	victim := all[0].sid
	info := m.infos[victim]
	delete(m.sandboxes, victim)
	delete(m.infos, victim)
	delete(m.lastActivity, victim)
	for tid, mapped := range m.threadSandboxes {
		if mapped == victim {
			delete(m.threadSandboxes, tid)
		}
	}
	_ = stopContainer(m.rt, info.ContainerID)
	return true
}

// reconcileOrphans seeds the warm pool with any containers a previous
// process left behind. Treated as warm (not active) because we don't
// know which thread they belong to.
func (m *Manager) reconcileOrphans() {
	for _, info := range listRunningContainers(m.rt, m.cfg.Sandbox.ContainerPrefix) {
		m.warmPool[info.SandboxID] = warmEntry{info: info, releasedAt: time.Now()}
	}
}

// buildMounts: bind-mount per-thread user-data dirs (so /mnt/user-data
// inside the container points at the host's thread dir) plus the static
// custom mounts the user configured.
func (m *Manager) buildMounts(ctx context.Context, tid string) []mountSpec {
	uid := runtime.GetEffectiveUserID(ctx)
	if err := config.EnsureThreadDirs(m.cfg, tid, uid); err != nil {
		m.log.Warn("aio: ensure thread dirs", "thread_id", tid, "error", err)
	}
	out := []mountSpec{}
	out = append(out, mountSpec{
		Host:      config.SandboxUserDataDir(m.cfg, tid, uid),
		Container: "/mnt/user-data",
		ReadOnly:  false,
	})
	for _, mt := range m.cfg.Sandbox.Mounts {
		out = append(out, mountSpec{Host: mt.HostPath, Container: mt.ContainerPath, ReadOnly: mt.ReadOnly})
	}
	return out
}

func init() {
	sandbox.RegisterAioFactory(New)
}
