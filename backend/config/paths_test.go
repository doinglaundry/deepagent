package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPaths_LayoutAndIsolation locks the .eino-cli/users/<uid>/threads/<tid>
// layout so the sandbox path-mapping code and the cleanup tooling can rely
// on a single derivation rule. Drift here would silently leak one user's
// thread data into another user's mount.
func TestPaths_LayoutAndIsolation(t *testing.T) {
	cfg := &Config{RootDir: "/tmp/eino-root"}

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
		"base":        BaseDir(cfg),
		"checkpoints": CheckpointsDir(cfg),
		"runs":        RunsDir(cfg),
		"rollback":    RollbackDir(cfg),
		"memory":      MemoryDir(cfg),
		"history":     InputHistoryPath(cfg),
		"log":         LogPath(cfg),
		"user":        UserDir(cfg, "alice"),
		"thread":      ThreadDir(cfg, "T1", "alice"),
		"user-data":   SandboxUserDataDir(cfg, "T1", "alice"),
		"workspace":   SandboxWorkDir(cfg, "T1", "alice"),
		"uploads":     SandboxUploadsDir(cfg, "T1", "alice"),
		"outputs":     SandboxOutputsDir(cfg, "T1", "alice"),
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: got %q, want %q", k, got[k], w)
		}
	}

	// Different uid must yield a disjoint subtree — sandbox path checks rely
	// on prefix matching for the reverse mask, so any path crossover breaks
	// the user-isolation invariant.
	alice := ThreadDir(cfg, "T1", "alice")
	bob := ThreadDir(cfg, "T1", "bob")
	if alice == bob {
		t.Fatalf("uid not separated: both resolve to %q", alice)
	}
	if strings.HasPrefix(alice, bob) || strings.HasPrefix(bob, alice) {
		t.Fatalf("uid subtrees overlap: alice=%q bob=%q", alice, bob)
	}
}

func TestVirtualPathPrefix(t *testing.T) {
	if VirtualPathPrefix != "/mnt/user-data" {
		t.Errorf("VirtualPathPrefix = %q, want %q", VirtualPathPrefix, "/mnt/user-data")
	}
}

// TestEnsureThreadDirs_Idempotent: calling twice must not error. tools may
// call EnsureThreadDirs on every Acquire (cheap; MkdirAll-only) — a second
// call returning ErrExist would crash the sandbox lifecycle.
func TestEnsureThreadDirs_Idempotent(t *testing.T) {
	root := t.TempDir()
	cfg := &Config{RootDir: root}

	for i := 0; i < 2; i++ {
		if err := EnsureThreadDirs(cfg, "T1", "alice"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	for _, dir := range []string{
		SandboxWorkDir(cfg, "T1", "alice"),
		SandboxUploadsDir(cfg, "T1", "alice"),
		SandboxOutputsDir(cfg, "T1", "alice"),
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

	// Sanity check that the dirs really nest under cfg.RootDir/.eino-cli —
	// catches a future refactor that accidentally rewrites BaseDir.
	wantPrefix := filepath.Join(root, ".eino-cli")
	if !strings.HasPrefix(SandboxWorkDir(cfg, "T1", "alice"), wantPrefix) {
		t.Errorf("workspace not under %q", wantPrefix)
	}
}
