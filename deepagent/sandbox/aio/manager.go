package aio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"eino-cli/deepagent/config"
	"eino-cli/deepagent/sandbox"
	"eino-cli/deepagent/sandbox/paths"
	"eino-cli/deepagent/utils/network"
)

// Manager owns the aio sandbox for one CLI session.
type Manager struct {
	cfg       *config.Config
	rt        containerRuntime
	log       *slog.Logger
	sessionID string
	sandboxID string
	mounts    []sandboxpaths.MountMapping

	mu           sync.Mutex
	sandboxes    map[string]*Sandbox
	infos        map[string]SandboxInfo
	lastActivity map[string]time.Time
	warmPool     map[string]warmEntry
	closed       bool

	gate       chan struct{}
	stopIdle   chan struct{}
	operations sync.WaitGroup
	shutdown   sync.Once
}

var errManagerShutdown = errors.New("aio manager is shut down")

// New builds the aio Manager bound to sessionID and seeds the warm pool from orphans.
func New(cfg *config.Config, sessionID string) (sandbox.SandboxManager, error) {
	if sessionID == "" {
		return nil, sandbox.ErrSessionIDRequired
	}
	err := config.ValidateSessionID(sessionID)
	if err != nil {
		return nil, err
	}
	if cfg == nil {
		return nil, fmt.Errorf("aio config is required")
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
		gate:         make(chan struct{}, 1),
	}
	if cfg.Sandbox.Use == "docker" {
		_, err := exec.LookPath("docker")
		if err != nil {
			return nil, fmt.Errorf("docker CLI is required: %w", err)
		}
		m.rt = runtimeDocker
	}
	if m.rt == "" {
		return nil, fmt.Errorf("aio: no container runtime (docker / container CLI)")
	}
	if cfg.Sandbox.Use != "docker" {
		m.mounts, err = sandboxpaths.BuildMountMappings(sessionID)
		if err != nil {
			return nil, err
		}
	}
	for _, mount := range cfg.Sandbox.Mounts {
		m.mounts = append(m.mounts, sandboxpaths.MountMapping{HostPath: mount.HostPath, VirtualPath: mount.ContainerPath, ReadOnly: mount.ReadOnly})
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
	return hex.EncodeToString(sum[:])
}

func (m *Manager) SessionID() string { return m.sessionID }

func (m *Manager) isClosed() bool {
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	return closed
}

func (m *Manager) GetSandboxIdBySessionId(ctx context.Context, sessionID string) (string, error) {
	if sessionID != "" && sessionID != m.sessionID {
		return "", fmt.Errorf("aio manager: session_id %q does not match %q", sessionID, m.sessionID)
	}
	if m.isClosed() {
		return "", errManagerShutdown
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-m.stopIdle:
		return "", errManagerShutdown
	case m.gate <- struct{}{}:
	}
	defer func() { <-m.gate }()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return "", errManagerShutdown
	}
	m.operations.Add(1)
	m.mu.Unlock()
	defer m.operations.Done()
	runCtx, stop := m.contextWithShutdown(ctx)
	defer stop()
	cached, ok := m.reuse()
	if ok {
		return cached, nil
	}
	return m.discoverOrCreate(runCtx, m.sandboxID)
}

func (m *Manager) reuse() (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return "", false
	}
	sid := m.sandboxID
	_, alive := m.sandboxes[sid]
	if alive {
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
	if m.closed || sid != m.sandboxID {
		return
	}
	client := newSandbox(sid, m.sessionID, info.SandboxURL, m.mounts)
	client.containerName = info.ContainerName
	client.runtime = m.rt
	m.sandboxes[sid] = client
	m.infos[sid] = info
	m.lastActivity[sid] = time.Now()
}

func (m *Manager) discoverOrCreate(ctx context.Context, sid string) (string, error) {
	if sid != m.sandboxID {
		return "", fmt.Errorf("aio manager: sandbox id %q is not owned by session", sid)
	}
	if m.isClosed() {
		return "", errManagerShutdown
	}
	lockPath := filepath.Join(os.TempDir(), "eino-sandbox-"+sid+".lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	for {
		flockErr := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if flockErr == nil {
			break
		}
		if !errors.Is(flockErr, unix.EWOULDBLOCK) {
			return "", flockErr
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()

	if m.isClosed() {
		return "", errManagerShutdown
	}
	info, ok := discoverContainer(ctx, m.rt, m.cfg.Sandbox.ContainerPrefix, sid)
	if ok {
		m.mu.Lock()
		closed := m.closed
		if !closed {
			m.attachLocked(sid, info)
		}
		m.mu.Unlock()
		if closed {
			_ = stopContainer(m.rt, info.ContainerID)
			return "", errManagerShutdown
		}
		return sid, nil
	}
	contextErr := ctx.Err()
	if contextErr != nil {
		return "", contextErr
	}
	return m.createSandbox(ctx, sid)
}

func (m *Manager) createSandbox(ctx context.Context, sid string) (string, error) {
	closed := m.isClosed()
	if closed {
		return "", errManagerShutdown
	}
	runCtx := ctx

	port, err := network.GetFreePort(8081)
	if err != nil {
		return "", err
	}
	name := m.cfg.Sandbox.ContainerPrefix + "-" + sid
	cid, err := startContainer(runCtx, containerSpec{
		Runtime: m.rt,
		Image:   m.cfg.Sandbox.Image,
		Name:    name,
		Port:    port,
		Mounts:  m.buildMounts(),
		Env:     m.cfg.Sandbox.Environment,
	})
	if err != nil {
		// Docker may have created the named container before the CLI was canceled.
		// No ID is available on this path, so clean up only our known name.
		if runCtx.Err() != nil {
			_ = stopContainer(m.rt, name)
		}
		if m.isClosed() {
			return "", errManagerShutdown
		}
		return "", err
	}
	info := SandboxInfo{
		SandboxID:     sid,
		SandboxURL:    fmt.Sprintf("http://localhost:%d", port),
		ContainerName: name,
		ContainerID:   cid,
		CreatedAt:     time.Now(),
	}
	readyCtx, cancel := context.WithTimeout(runCtx, 60*time.Second)
	defer cancel()
	readyErr := waitReady(readyCtx, info.SandboxURL)
	if readyErr != nil {
		_ = stopContainer(m.rt, cid)
		if m.isClosed() {
			return "", errManagerShutdown
		}
		return "", fmt.Errorf("sandbox %s not ready: %w", sid, readyErr)
	}
	m.mu.Lock()
	closed = m.closed
	if !closed {
		m.attachLocked(sid, info)
	}
	m.mu.Unlock()
	if closed {
		_ = stopContainer(m.rt, cid)
		return "", errManagerShutdown
	}
	return sid, nil
}

func (m *Manager) contextWithShutdown(ctx context.Context) (context.Context, func()) {
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		select {
		case <-m.stopIdle:
			cancel()
		case <-done:
		}
	}()
	stop := func() {
		close(done)
		cancel()
	}
	return runCtx, stop
}

// Get returns the live Sandbox for sid.
func (m *Manager) Get(ctx context.Context, sid string) (sandbox.Sandbox, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || sid != m.sandboxID {
		return nil, sandbox.NewNotFoundError(sid)
	}
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
	if m.closed || sid != m.sandboxID {
		return nil
	}
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
		m.closed = true
		containerIDs := make([]string, 0, len(m.infos)+len(m.warmPool))
		for sid, info := range m.infos {
			if sid == m.sandboxID {
				containerIDs = append(containerIDs, info.ContainerID)
			}
		}
		for sid, entry := range m.warmPool {
			if sid == m.sandboxID {
				containerIDs = append(containerIDs, entry.info.ContainerID)
			}
		}
		m.sandboxes = map[string]*Sandbox{}
		m.infos = map[string]SandboxInfo{}
		m.lastActivity = map[string]time.Time{}
		m.warmPool = map[string]warmEntry{}
		m.mu.Unlock()
		m.operations.Wait()
		for _, containerID := range containerIDs {
			var stopErr error
			for attempt := 0; attempt < 3; attempt++ {
				stopErr = stopContainer(m.rt, containerID)
				if stopErr == nil {
					break
				}
			}
			if stopErr != nil {
				m.log.Error("aio: container cleanup failed", "container_id", containerID, "error", stopErr)
			}
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

// Cleanup and acquisition share the same gate; container commands never hold mu.
func (m *Manager) cleanupIdle() {
	if m.cfg.Sandbox.IdleTimeout <= 0 {
		return
	}
	select {
	case m.gate <- struct{}{}:
	default:
		return
	}
	defer func() { <-m.gate }()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.operations.Add(1)
	defer m.operations.Done()
	sid := m.sandboxID
	cutoff := time.Now().Add(-m.cfg.Sandbox.IdleTimeout)
	last, active := m.lastActivity[sid]
	if active && last.Before(cutoff) {
		m.warmPool[sid] = warmEntry{info: m.infos[sid], releasedAt: time.Now()}
		delete(m.sandboxes, sid)
		delete(m.infos, sid)
		delete(m.lastActivity, sid)
	}
	entry, warm := m.warmPool[sid]
	m.mu.Unlock()
	if !warm || !entry.releasedAt.Before(cutoff) {
		return
	}
	err := stopContainer(m.rt, entry.info.ContainerID)
	if err != nil {
		return
	} // Keep failed removals for a later retry.
	m.mu.Lock()
	delete(m.warmPool, sid)
	m.mu.Unlock()
}

// Inspect only this manager's exact container; never import a shared prefix pool.
func (m *Manager) reconcileOrphans() {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	info, ok := discoverContainer(ctx, m.rt, m.cfg.Sandbox.ContainerPrefix, m.sandboxID)
	if ok {
		m.warmPool[m.sandboxID] = warmEntry{info: info, releasedAt: time.Now()}
	}
}

// buildMounts converts the validated session mounts to container arguments.
func (m *Manager) buildMounts() []mountSpec {
	out := make([]mountSpec, 0, len(m.mounts))
	for _, mount := range m.mounts {
		out = append(out, mountSpec{Host: mount.HostPath, Container: mount.VirtualPath, ReadOnly: mount.ReadOnly})
	}
	return out
}
