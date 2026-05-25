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
	"eino-cli/backend/sandbox"
	"eino-cli/backend/sandboxpaths"
	"eino-cli/backend/util/network"
)

// Manager owns the aio sandbox for one CLI session.
type Manager struct {
	cfg       *config.Config
	rt        containerRuntime
	log       *slog.Logger
	sessionID string
	sandboxID string

	mu           sync.Mutex
	sandboxes    map[string]*Sandbox
	infos        map[string]SandboxInfo
	lastActivity map[string]time.Time
	warmPool     map[string]warmEntry

	sf       singleflight.Group
	stopIdle chan struct{}
	shutdown sync.Once
}

// New builds the aio Manager bound to sessionID and seeds the warm pool from orphans.
func New(cfg *config.Config, sessionID string) (sandbox.SandboxManager, error) {
	if sessionID == "" {
		return nil, sandbox.ErrSessionIDRequired
	}
	m := &Manager{
		cfg:          cfg,
		rt:           detectRuntime(),
		log:          slog.Default(),
		sessionID:    sessionID,
		sandboxID:    deriveSandboxID(sessionID),
		sandboxes:    map[string]*Sandbox{},
		infos:        map[string]SandboxInfo{},
		lastActivity: map[string]time.Time{},
		warmPool:     map[string]warmEntry{},
		stopIdle:     make(chan struct{}),
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

func (m *Manager) SessionID() string { return m.sessionID }

func (m *Manager) GetSandboxIdBySessionId(ctx context.Context, sessionID string) (string, error) {
	if sessionID != "" && sessionID != m.sessionID {
		return "", fmt.Errorf("aio manager: session_id %q does not match %q", sessionID, m.sessionID)
	}
	v, err, _ := m.sf.Do(m.sessionID, func() (any, error) {
		if cached, ok := m.reuse(); ok {
			return cached, nil
		}
		return m.discoverOrCreate(ctx, m.sandboxID)
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}

func (m *Manager) reuse() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sid := m.sandboxID
	if _, alive := m.sandboxes[sid]; alive {
		m.lastActivity[sid] = time.Now()
		return sid, true
	}
	entry, ok := m.warmPool[sid]
	if !ok {
		return "", false
	}
	delete(m.warmPool, sid)
	m.attachLocked(sid, entry.info)
	return sid, true
}

func (m *Manager) attachLocked(sid string, info SandboxInfo) {
	mounts, _ := sandboxpaths.BuildMountMappings(m.sessionID)
	m.sandboxes[sid] = newSandbox(sid, m.sessionID, info.SandboxURL, mounts)
	m.infos[sid] = info
	m.lastActivity[sid] = time.Now()
}

func (m *Manager) discoverOrCreate(ctx context.Context, sid string) (string, error) {
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
		m.attachLocked(sid, info)
		m.mu.Unlock()
		return sid, nil
	}
	return m.createSandbox(ctx, sid)
}

func (m *Manager) createSandbox(ctx context.Context, sid string) (string, error) {
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
		Mounts:  m.buildMounts(ctx),
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
	m.attachLocked(sid, info)
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
	m.warmPool[sid] = warmEntry{info: info, releasedAt: time.Now()}
	return nil
}

// Reset drops all in-process state; containers themselves are untouched.
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sandboxes = map[string]*Sandbox{}
	m.infos = map[string]SandboxInfo{}
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
	_ = stopContainer(m.rt, info.ContainerID)
	return true
}

// reconcileOrphans seeds the warm pool with containers a previous process left behind.
func (m *Manager) reconcileOrphans() {
	for _, info := range listRunningContainers(m.rt, m.cfg.Sandbox.ContainerPrefix) {
		m.warmPool[info.SandboxID] = warmEntry{info: info, releasedAt: time.Now()}
	}
}

// buildMounts assembles per-session mounts from sandboxpaths.BuildMountMappings.
func (m *Manager) buildMounts(ctx context.Context) []mountSpec {
	mounts, err := sandboxpaths.BuildMountMappings(m.sessionID)
	if err != nil {
		m.log.Warn("aio: build mount mappings", "session_id", m.sessionID, "error", err)
		return nil
	}
	out := make([]mountSpec, 0, len(mounts))
	for _, mm := range mounts {
		out = append(out, mountSpec{Host: mm.HostPath, Container: mm.VirtualPath, ReadOnly: mm.ReadOnly})
	}
	return out
}
