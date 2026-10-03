package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/deepagent/constant"
)

func TestPaths_Layout(t *testing.T) {
	cleanup := SetRootDirForTest("/tmp/eino-root")
	defer cleanup()

	sid := "T1"
	want := map[string]string{
		"base":        "/tmp/eino-root/.eino-cli",
		"session":     "/tmp/eino-root/.eino-cli/sessions/T1",
		"runs":        "/tmp/eino-root/.eino-cli/sessions/T1/runs",
		"rollback":    "/tmp/eino-root/.eino-cli/sessions/T1/rollback",
		"checkpoints": "/tmp/eino-root/.eino-cli/sessions/T1/checkpoints",
		"workspace":   "/tmp/eino-root/.eino-cli/sessions/T1/workspace",
		"uploads":     "/tmp/eino-root/.eino-cli/sessions/T1/uploads",
		"outputs":     "/tmp/eino-root/.eino-cli/sessions/T1/outputs",
		"memory":      "/tmp/eino-root/.eino-cli/memory",
	}
	got := map[string]string{
		"base":        BaseDir(),
		"session":     SessionTreeDir(sid),
		"runs":        SessionRunsDir(sid),
		"rollback":    SessionRollbackDir(sid),
		"checkpoints": SessionCheckpointsDir(sid),
		"workspace":   SandboxWorkDir(sid),
		"uploads":     SandboxUploadsDir(sid),
		"outputs":     SandboxOutputsDir(sid),
		"memory":      MemoryDir(),
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %q, want %q", k, got[k], w)
		}
	}
}

func TestDefaultSessionID(t *testing.T) {
	if consts.DefaultSessionID != "default_session_id" {
		t.Errorf("DefaultSessionID = %q", consts.DefaultSessionID)
	}
}

func TestEnsureSessionDirs_Idempotent(t *testing.T) {
	root := t.TempDir()
	cleanup := SetRootDirForTest(root)
	defer cleanup()

	for i := 0; i < 2; i++ {
		err := EnsureSessionDirs("T1")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	for _, dir := range []string{
		SandboxWorkDir("T1"),
		SandboxUploadsDir("T1"),
		SandboxOutputsDir("T1"),
		SessionRunsDir("T1"),
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("stat %q: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%q is not a dir", dir)
		}
	}

	wantPrefix := filepath.Join(root, ".eino-cli")
	if !strings.HasPrefix(SandboxWorkDir("T1"), wantPrefix) {
		t.Errorf("workspace not under %q", wantPrefix)
	}
}

func TestEnsureSessionDirsRejectsUnsafeIDsBeforeCreation(t *testing.T) {
	root := t.TempDir()
	cleanup := SetRootDirForTest(root)
	defer cleanup()

	for _, sessionID := range []string{"", ".", "..", "../../outside", "nested/session", "/absolute"} {
		err := EnsureSessionDirs(sessionID)
		if err == nil {
			t.Errorf("EnsureSessionDirs(%q) succeeded", sessionID)
		}
	}

	_, err := os.Stat(filepath.Join(root, ".eino-cli"))
	if !os.IsNotExist(err) {
		t.Fatalf("invalid IDs created the base directory: %v", err)
	}
}

func TestEnsureSessionDirsRejectsSymlinkedSession(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	cleanup := SetRootDirForTest(root)
	defer cleanup()

	sessionsDir := filepath.Join(root, ".eino-cli", "sessions")
	err := os.MkdirAll(sessionsDir, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(sessionsDir, "T1")
	err = os.Symlink(outside, sessionDir)
	if err != nil {
		t.Skipf("symlink unsupported on this filesystem: %v", err)
	}

	err = EnsureSessionDirs("T1")
	if err == nil {
		t.Fatal("expected symlinked session to be rejected")
	}
	_, err = os.Stat(filepath.Join(outside, "workspace"))
	if !os.IsNotExist(err) {
		t.Fatalf("symlink target was modified: %v", err)
	}
}
