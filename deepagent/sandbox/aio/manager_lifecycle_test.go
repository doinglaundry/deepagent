package aio

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/config"
	"errors"
	"golang.org/x/sys/unix"
)

func TestNewScopesOrphansToOwnSandbox(t *testing.T) {
	prefix := "eino-test-sandbox"
	sessionID := "session-own"
	ownID := deriveSandboxID(sessionID)
	foreignID := deriveSandboxID("session-foreign")
	names := []string{
		prefix + "-" + ownID,
		prefix + "-" + foreignID,
		"other-" + prefix + "-" + foreignID,
	}
	logPath := installFakeDocker(t, names)

	manager, err := New(&config.Config{Sandbox: config.SandboxConfig{
		Use:             "docker",
		Image:           "test-image",
		ContainerPrefix: prefix,
	}}, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := manager.(*Manager)
	if !ok {
		t.Fatalf("manager type = %T", manager)
	}
	if len(m.warmPool) != 1 {
		t.Fatalf("warm pool size = %d, want 1", len(m.warmPool))
	}
	_, ok = m.warmPool[ownID]
	if !ok {
		t.Fatalf("warm pool does not contain own sandbox %q", ownID)
	}
	_, ok = m.warmPool[foreignID]
	if ok {
		t.Fatalf("warm pool contains foreign sandbox %q", foreignID)
	}

	m.Shutdown()
	lines := readFakeDockerLog(t, logPath)
	wantRemove := "rm -f " + prefix + "-" + ownID
	if countLine(lines, wantRemove) != 1 {
		t.Fatalf("remove commands = %v, want one %q", lines, wantRemove)
	}
	foreignRemove := "rm -f " + prefix + "-" + foreignID
	if countLine(lines, foreignRemove) != 0 {
		t.Fatalf("remove commands = %v, unexpectedly removed foreign sandbox", lines)
	}
}

func TestShutdownIsTerminalAndClearsState(t *testing.T) {
	prefix := "eino-test-sandbox"
	sessionID := "session-shutdown"
	logPath := installFakeDocker(t, nil)
	manager, err := New(&config.Config{Sandbox: config.SandboxConfig{
		Use:             "docker",
		Image:           "test-image",
		ContainerPrefix: prefix,
	}}, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := manager.(*Manager)
	if !ok {
		t.Fatalf("manager type = %T", manager)
	}
	m.mu.Lock()
	m.attachLocked(m.sandboxID, SandboxInfo{
		SandboxID:     m.sandboxID,
		SandboxURL:    "http://localhost:18080",
		ContainerName: prefix + "-" + m.sandboxID,
		ContainerID:   "owned-container",
	})
	m.mu.Unlock()

	m.Shutdown()
	if len(m.sandboxes) != 0 || len(m.infos) != 0 || len(m.lastActivity) != 0 || len(m.warmPool) != 0 {
		t.Fatalf("shutdown state: sandboxes=%d infos=%d activity=%d warm=%d", len(m.sandboxes), len(m.infos), len(m.lastActivity), len(m.warmPool))
	}
	_, err = m.Get(context.Background(), m.sandboxID)
	if err == nil {
		t.Fatal("Get returned a provider after shutdown")
	}
	_, err = m.GetSandboxIdBySessionId(context.Background(), sessionID)
	if err == nil {
		t.Fatal("acquisition succeeded after shutdown")
	}

	m.Reset()
	before := len(readFakeDockerLog(t, logPath))
	_, err = m.GetSandboxIdBySessionId(context.Background(), sessionID)
	if err == nil {
		t.Fatal("acquisition succeeded after shutdown and Reset")
	}
	after := len(readFakeDockerLog(t, logPath))
	if after != before {
		t.Fatalf("shutdown acquisition issued new container commands: before=%d after=%d", before, after)
	}
	lines := readFakeDockerLog(t, logPath)
	if countLine(lines, "rm -f owned-container") != 1 {
		t.Fatalf("remove commands = %v, want one owned-container removal", lines)
	}
}

func TestLifecycleOperationsIgnoreForeignSandboxState(t *testing.T) {
	prefix := "eino-test-sandbox"
	sessionID := "session-scope"
	logPath := installFakeDocker(t, nil)
	manager, err := New(&config.Config{Sandbox: config.SandboxConfig{
		Use:             "docker",
		Image:           "test-image",
		ContainerPrefix: prefix,
	}}, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	m, ok := manager.(*Manager)
	if !ok {
		t.Fatalf("manager type = %T", manager)
	}
	foreignID := deriveSandboxID("session-foreign-scope")
	foreignInfo := SandboxInfo{SandboxID: foreignID, ContainerID: "foreign-container"}
	old := time.Now().Add(-time.Hour)
	m.cfg.Sandbox.IdleTimeout = time.Minute
	m.mu.Lock()
	m.sandboxes[foreignID] = newSandbox(foreignID, "session-foreign-scope", "http://localhost:18080", nil)
	m.infos[foreignID] = foreignInfo
	m.lastActivity[foreignID] = old
	m.warmPool[foreignID] = warmEntry{info: foreignInfo, releasedAt: old}
	m.mu.Unlock()

	m.cleanupIdle()
	_, ok = m.sandboxes[foreignID]
	if !ok {
		t.Fatal("cleanup removed foreign active sandbox")
	}
	_, ok = m.warmPool[foreignID]
	if !ok {
		t.Fatal("cleanup removed foreign warm sandbox")
	}

	m.cfg.Sandbox.Replicas = 1
	m.mu.Lock()
	m.sandboxes[m.sandboxID] = newSandbox(m.sandboxID, sessionID, "http://localhost:18080", nil)
	m.infos[m.sandboxID] = SandboxInfo{SandboxID: m.sandboxID, ContainerID: "owned-active"}
	m.lastActivity[m.sandboxID] = time.Now()
	m.mu.Unlock()
	_, ok = m.warmPool[foreignID]
	if !ok {
		t.Fatal("eviction removed foreign warm sandbox")
	}

	m.Shutdown()
	lines := readFakeDockerLog(t, logPath)
	if countLine(lines, "rm -f foreign-container") != 0 {
		t.Fatalf("foreign container was removed: %v", lines)
	}
	if countLine(lines, "rm -f owned-active") != 1 {
		t.Fatalf("owned active container was not evicted: %v", lines)
	}
}

func installFakeDocker(t *testing.T, names []string) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "docker.log")
	nameArgs := strings.Join(names, " ")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> "$FAKE_DOCKER_LOG"
case "$1" in
ps)
  printf '%%s\n' %s
  ;;
inspect)
  case " %s " in *" $2 "*) ;; *) exit 1;; esac
  printf '%%s\n' '[{"NetworkSettings":{"Ports":{"8080/tcp":[{"HostPort":"18080"}]}},"Created":"2026-01-01T00:00:00Z"}]'
  ;;
rm)
  ;;
run)
  printf 'fake-container-id\n'
  ;;
esac
`, nameArgs, nameArgs)
	dockerPath := filepath.Join(dir, "docker")
	err := os.WriteFile(dockerPath, []byte(script), 0o700)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("FAKE_DOCKER_LOG", logPath)
	return logPath
}

func readFakeDockerLog(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.TrimSpace(string(raw))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func countLine(lines []string, want string) int {
	count := 0
	for _, line := range lines {
		if line == want {
			count++
		}
	}
	return count
}

func TestNewRejectsUnsafeSessionBeforeContainerCommands(t *testing.T) {
	logPath := installFakeDocker(t, nil)
	for _, id := range []string{".", "..", "nested/session", "../other"} {
		_, err := New(&config.Config{Sandbox: config.SandboxConfig{Use: "docker"}}, id)
		if err == nil {
			t.Fatalf("accepted session %q", id)
		}
	}
	_, err := os.Stat(logPath)
	if !os.IsNotExist(err) {
		t.Fatalf("invalid session reached container CLI: %v", err)
	}
}

func TestNewFailsBeforeContainerCommandsWhenMountsCannotBeBuilt(t *testing.T) {
	logPath := installFakeDocker(t, nil)
	root := t.TempDir()
	restore := config.SetRootDirForTest(root)
	defer restore()
	err := os.WriteFile(filepath.Join(root, ".eino-cli"), []byte("not a directory"), 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(&config.Config{}, "session")
	if err == nil {
		t.Fatal("accepted unusable session mounts")
	}
	_, err = os.Stat(logPath)
	if !os.IsNotExist(err) {
		t.Fatalf("mount error reached container CLI: %v", err)
	}
}

func TestShutdownCancelsPendingContainerCreation(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "creating")
	log := filepath.Join(dir, "removed")
	script := `#!/bin/sh
case "$1" in
ps) exit 0;;
inspect) exit 1;;
run) printf 'creating' > "$CREATING"; exec /bin/sleep 30;;
rm) printf '%s' "$3" > "$REMOVED";;
esac
`
	err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("CREATING", marker)
	t.Setenv("REMOVED", log)
	manager, err := New(&config.Config{Sandbox: config.SandboxConfig{Use: "docker", Image: "fake", ContainerPrefix: "owned"}}, "pending")
	if err != nil {
		t.Fatal(err)
	}
	m := manager.(*Manager)
	defer m.Shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, acquireErr := m.GetSandboxIdBySessionId(ctx, "pending"); result <- acquireErr }()
	for {
		_, err = os.Stat(marker)
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("container creation did not start")
		case <-time.After(time.Millisecond):
		}
	}
	stopped := make(chan struct{})
	go func() { m.Shutdown(); close(stopped) }()
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("shutdown did not cancel pending creation")
	}
	select {
	case err = <-result:
		if err == nil {
			t.Fatal("acquisition succeeded after shutdown")
		}
	case <-ctx.Done():
		t.Fatal("acquisition did not finish")
	}
	removed, err := os.ReadFile(log)
	expected := "owned-" + deriveSandboxID("pending")
	if err != nil || string(removed) != expected {
		t.Fatalf("uncertain container was not cleaned: removed=%q error=%v", removed, err)
	}
}

func TestAcquireCancellationWhileWaitingForFileLock(t *testing.T) {
	installFakeDocker(t, nil)
	manager, err := New(&config.Config{Sandbox: config.SandboxConfig{Use: "docker", ContainerPrefix: "lock-test"}}, t.Name())
	if err != nil {
		t.Fatal(err)
	}
	m := manager.(*Manager)
	defer m.Shutdown()
	file, err := os.OpenFile(filepath.Join(os.TempDir(), "eino-sandbox-"+m.sandboxID+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Flock(int(file.Fd()), unix.LOCK_UN)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { _, err := m.GetSandboxIdBySessionId(ctx, t.Name()); result <- err }()
	select {
	case err = <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("acquisition ignored deadline while waiting for lock")
	}
	m.gate <- struct{}{}
	waiter, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	_, err = m.GetSandboxIdBySessionId(waiter, t.Name())
	stop()
	<-m.gate
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting acquisition ignored own deadline: %v", err)
	}
}
