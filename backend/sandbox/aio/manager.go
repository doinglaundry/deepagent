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

// Manager owns every aio sandbox in this process.
type Manager struct {
	cfg *config.Config
	rt  containerRuntime
	log *slog.Logger

	mu              sync.Mutex
	sandboxes       map[string]*Sandbox
	infos           map[string]SandboxInfo
	sessionSandboxes map[string]string
	lastActivity    map[string]time.Time
	warmPool        map[string]warmEntry

	sf       singleflight.Group
	stopIdle chan struct{}
	shutdown sync.Once
}

// New builds the aio Manager and seeds the warm pool from any orphan containers.
func New(cfg *config.Config) (sandbox.SandboxManager, error) {
	m := &Manager{
		cfg:             cfg,
		rt:              detectRuntime(),
		log:             slog.Default(),
		sandboxes:       map[string]*Sandbox{},
		infos:           map[string]SandboxInfo{},
		sessionSandboxes: map[string]string{},
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

// Same sessionID hashes to the same sid in every process, so container names collide on purpose.
func deriveSandboxID(sessionID string) string {
	sum := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(sum[:])[:8]
}

// Acquire returns the sid for sessionID; singleflight prevents concurrent cold-starts.
func (m *Manager) Acquire(ctx context.Context, sessionID string) (string, error) {
	if sessionID == "" {
		return "", sandbox.ErrSessionIDRequired
	}
	v, err, _ := m.sf.Do(sessionID, func() (any, error) {
		sid := deriveSandboxID(sessionID)
		if cached, ok := m.reuse(sessionID, sid); ok {
			return cached, nil
		}
		return m.discoverOrCreate(ctx, sessionID, sid)
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

// reuse checks the in-process cache and warm pool under one lock.
func (m *Manager) reuse(sessionID, sid string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cached, ok := m.sessionSandboxes[sessionID]; ok {
		if _, alive := m.sandboxes[cached]; alive {
			m.lastActivity[cached] = time.Now()
			return cached, true
		}
		delete(m.sessionSandboxes, sessionID)
	}
	entry, ok := m.warmPool[sid]
	if !ok {
		return "", false
	}
	delete(m.warmPool, sid)
	m.attachLocked(sessionID, sid, entry.info)
	return sid, true
}

// attachLocked records the sandbox in every map; caller must hold m.mu.
func (m *Manager) attachLocked(sessionID, sid string, info SandboxInfo) {
	m.sandboxes[sid] = newSandbox(sid, info.SandboxURL)
	m.infos[sid] = info
	if sessionID != "" {
		m.sessionSandboxes[sessionID] = sid
	}
	m.lastActivity[sid] = time.Now()
}

// discoverOrCreate adopts a peer-started container or starts one; flock blocks racing siblings.
func (m *Manager) discoverOrCreate(ctx context.Context, sessionID, sid string) (string, error) {
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
		m.attachLocked(sessionID, sid, info)
		m.mu.Unlock()
		return sid, nil
	}
	return m.createSandbox(ctx, sessionID, sid)
}

func (m *Manager) createSandbox(ctx context.Context, sessionID, sid string) (string, error) {
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
		Mounts:  m.buildMounts(ctx, sessionID),
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
	m.attachLocked(sessionID, sid, info)
	m.mu.Unlock()
	return sid, nil
}

// Get returns the live Sandbox for sid.
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

// Release moves sid to the warm pool so re-acquire skips the cold start.
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
	for sessionID, mapped := range m.sessionSandboxes {
		if mapped == sid {
			delete(m.sessionSandboxes, sessionID)
		}
	}
	m.warmPool[sid] = warmEntry{info: info, releasedAt: time.Now()}
	return nil
}

// Reset drops all in-process state; containers themselves are untouched.
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sandboxes = map[string]*Sandbox{}
	m.infos = map[string]SandboxInfo{}
	m.sessionSandboxes = map[string]string{}
	m.lastActivity = map[string]time.Time{}
	m.warmPool = map[string]warmEntry{}
}

// UsesSessionDataMounts reports true — aio bind-mounts per-session dirs.
func (m *Manager) UsesSessionDataMounts() bool { return true }

func (m *Manager) AllowsIsolatedExec() bool { return true }

// Shutdown is idempotent; tears down every container the manager knows about.
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

// cleanupIdle demotes active sandboxes past their TTL and stops over-aged warm ones.
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
			for sessionID, mapped := range m.sessionSandboxes {
				if mapped == sid {
					delete(m.sessionSandboxes, sessionID)
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

// evictUntilWithinReplicasLocked enforces cfg.Replicas; warm pool first, then oldest active.
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
	for sessionID, mapped := range m.sessionSandboxes {
		if mapped == victim {
			delete(m.sessionSandboxes, sessionID)
		}
	}
	_ = stopContainer(m.rt, info.ContainerID)
	return true
}

// reconcileOrphans seeds the warm pool with containers a previous process left behind.
func (m *Manager) reconcileOrphans() {
	for _, info := range listRunningContainers(m.rt, m.cfg.Sandbox.ContainerPrefix) {
		m.warmPool[info.SandboxID] = warmEntry{info: info, releasedAt: time.Now()}
	}
}

// buildMounts assembles per-session /mnt/user-data plus user-configured mounts.
func (m *Manager) buildMounts(ctx context.Context, sessionID string) []mountSpec {
	uid := runtime.GetEffectiveUserID(ctx)
	if err := config.EnsureSessionDirs(sessionID, uid); err != nil {
		m.log.Warn("aio: ensure session dirs", "session_id", sessionID, "error", err)
	}
	out := []mountSpec{}
	out = append(out, mountSpec{
		Host:      config.SandboxUserDataDir(sessionID, uid),
		Container: "/mnt/user-data",
		ReadOnly:  false,
	})

	return out
}
