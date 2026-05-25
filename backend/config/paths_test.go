package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/backend/consts"
)

func TestPaths_LayoutAndIsolation(t *testing.T) {
	cleanup := SetRootDirForTest("/tmp/eino-root")
	defer cleanup()

	sid := "T1"
	want := map[string]string{
		"base":         "/tmp/eino-root/.eino-cli",
		"session-tree": "/tmp/eino-root/.eino-cli/sessions/T1",
		"runs":         "/tmp/eino-root/.eino-cli/sessions/T1/runs",
		"rollback":     "/tmp/eino-root/.eino-cli/sessions/T1/rollback",
		"checkpoints":  "/tmp/eino-root/.eino-cli/sessions/T1/checkpoints",
		"memory":       "/tmp/eino-root/.eino-cli/memory",
		"user":         "/tmp/eino-root/.eino-cli/users/alice",
		"session":      "/tmp/eino-root/.eino-cli/users/alice/sessions/T1",
		"user-data":    "/tmp/eino-root/.eino-cli/users/alice/sessions/T1/user-data",
		"workspace":    "/tmp/eino-root/.eino-cli/users/alice/sessions/T1/user-data/workspace",
		"uploads":      "/tmp/eino-root/.eino-cli/users/alice/sessions/T1/user-data/uploads",
		"outputs":      "/tmp/eino-root/.eino-cli/users/alice/sessions/T1/user-data/outputs",
	}
	got := map[string]string{
		"base":         BaseDir(),
		"session-tree": SessionTreeDir(sid),
		"runs":         SessionRunsDir(sid),
		"rollback":     SessionRollbackDir(sid),
		"checkpoints":  SessionCheckpointsDir(sid),
		"memory":       MemoryDir(),
		"user":         UserDir("alice"),
		"session":      SessionDir(sid, "alice"),
		"user-data":    SandboxUserDataDir(sid, "alice"),
		"workspace":    SandboxWorkDir(sid, "alice"),
		"uploads":      SandboxUploadsDir(sid, "alice"),
		"outputs":      SandboxOutputsDir(sid, "alice"),
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %q, want %q", k, got[k], w)
		}
	}

	alice := SessionDir(sid, "alice")
	bob := SessionDir(sid, "bob")
	if alice == bob {
		t.Fatalf("uid not separated: both resolve to %q", alice)
	}
	if strings.HasPrefix(alice, bob) || strings.HasPrefix(bob, alice) {
		t.Fatalf("uid subtrees overlap: alice=%q bob=%q", alice, bob)
	}
}

func TestVirtualPathPrefix(t *testing.T) {
	if consts.VirtualPathPrefix != "/mnt/user-data" {
		t.Errorf("VirtualPathPrefix = %q, want %q", consts.VirtualPathPrefix, "/mnt/user-data")
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
		if err := EnsureSessionDirs("T1", "alice"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	for _, dir := range []string{
		SandboxWorkDir("T1", "alice"),
		SandboxUploadsDir("T1", "alice"),
		SandboxOutputsDir("T1", "alice"),
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
	if !strings.HasPrefix(SandboxWorkDir("T1", "alice"), wantPrefix) {
		t.Errorf("workspace not under %q", wantPrefix)
	}
}
