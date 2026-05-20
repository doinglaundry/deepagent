package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/backend/consts"
)

// TestPaths_LayoutAndIsolation locks the .eino-cli/users/<uid>/threads/<tid>
// layout so the sandbox path-mapping code and the cleanup tooling can rely
// on a single derivation rule. Drift here would silently leak one user's
// thread data into another user's mount.
func TestPaths_LayoutAndIsolation(t *testing.T) {
	cleanup := SetRootDirForTest("/tmp/eino-root")
	defer cleanup()

	want := map[string]string{
		"base":        "/tmp/eino-root/.eino-cli",
		"checkpoints": "/tmp/eino-root/.eino-cli/checkpoints",
		"runs":        "/tmp/eino-root/.eino-cli/runs",
		"rollback":    "/tmp/eino-root/.eino-cli/rollback",
		"memory":      "/tmp/eino-root/.eino-cli/memory",
		"history":     "/tmp/eino-root/.eino-cli/history.txt",
		"log":         "/tmp/eino-root/.eino-cli/eino-cli.log",
		"user":        "/tmp/eino-root/.eino-cli/users/alice",
		"thread":      "/tmp/eino-root/.eino-cli/users/alice/threads/T1",
		"user-data":   "/tmp/eino-root/.eino-cli/users/alice/threads/T1/user-data",
		"workspace":   "/tmp/eino-root/.eino-cli/users/alice/threads/T1/user-data/workspace",
		"uploads":     "/tmp/eino-root/.eino-cli/users/alice/threads/T1/user-data/uploads",
		"outputs":     "/tmp/eino-root/.eino-cli/users/alice/threads/T1/user-data/outputs",
	}
	got := map[string]string{
		"base":        BaseDir(),
		"checkpoints": CheckpointsDir(),
		"runs":        RunsDir(),
		"rollback":    RollbackDir(),
		"memory":      MemoryDir(),
		"history":     InputHistoryPath(),
		"log":         LogPath(),
		"user":        UserDir("alice"),
		"thread":      ThreadDir("T1", "alice"),
		"user-data":   SandboxUserDataDir("T1", "alice"),
		"workspace":   SandboxWorkDir("T1", "alice"),
		"uploads":     SandboxUploadsDir("T1", "alice"),
		"outputs":     SandboxOutputsDir("T1", "alice"),
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %q, want %q", k, got[k], w)
		}
	}

	// Different uid must yield a disjoint subtree — sandbox path checks rely
	// on prefix matching for the reverse mask, so any path crossover breaks
	// the user-isolation invariant.
	alice := ThreadDir("T1", "alice")
	bob := ThreadDir("T1", "bob")
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

// TestEnsureThreadDirs_Idempotent: calling twice must not error. tools may
// call EnsureThreadDirs on every Acquire (cheap; MkdirAll-only) — a second
// call returning ErrExist would crash the sandbox lifecycle.
func TestEnsureThreadDirs_Idempotent(t *testing.T) {
	root := t.TempDir()
	cleanup := SetRootDirForTest(root)
	defer cleanup()

	for i := 0; i < 2; i++ {
		if err := EnsureThreadDirs("T1", "alice"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	for _, dir := range []string{
		SandboxWorkDir("T1", "alice"),
		SandboxUploadsDir("T1", "alice"),
		SandboxOutputsDir("T1", "alice"),
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

	// Sanity check that the dirs really nest under RootDir()/.eino-cli —
	// catches a future refactor that accidentally rewrites BaseDir.
	wantPrefix := filepath.Join(root, ".eino-cli")
	if !strings.HasPrefix(SandboxWorkDir("T1", "alice"), wantPrefix) {
		t.Errorf("workspace not under %q", wantPrefix)
	}
}
