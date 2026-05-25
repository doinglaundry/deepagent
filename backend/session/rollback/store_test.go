package rollback

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/backend/config"
	"eino-cli/backend/consts"
)

func TestStoreSavePostRestorePost(t *testing.T) {
	root := t.TempDir()
	cleanup := config.SetRootDirForTest(root)
	defer cleanup()
	sid := consts.DefaultSessionID
	writeTestFile(t, root, ".eino-cli/sessions/"+sid+"/checkpoints/state.json", "checkpoint-1")
	writeTestFile(t, root, ".eino-cli/users/local/sessions/"+sid+"/user-data/data.txt", "user-data-1")
	writeTestFile(t, root, ".eino-cli/memory/memory.json", "memory-1")
	writeTestFile(t, root, ".eino-cli/skill-build/artifact.txt", "artifact-1")
	writeTestFile(t, root, ".eino-cli/sessions/"+sid+"/runs/run-1.json", "run-1")

	store := NewStore(root, sid)
	if _, err := store.SavePost(context.Background(), "run-1", []byte(`["history-1"]`)); err != nil {
		t.Fatalf("SavePost() error = %v", err)
	}

	writeTestFile(t, root, ".eino-cli/sessions/"+sid+"/checkpoints/state.json", "checkpoint-2")
	writeTestFile(t, root, ".eino-cli/memory/memory.json", "memory-2")
	writeTestFile(t, root, ".eino-cli/skill-extra/artifact.txt", "artifact-2")
	writeTestFile(t, root, ".eino-cli/sessions/"+sid+"/runs/run-2.json", "run-2")

	history, err := store.RestorePost(context.Background(), "run-1")
	if err != nil {
		t.Fatalf("RestorePost() error = %v", err)
	}
	if !strings.Contains(string(history), "history-1") {
		t.Fatalf("history = %s", history)
	}
	assertTestFile(t, root, ".eino-cli/sessions/"+sid+"/checkpoints/state.json", "checkpoint-1")
	assertTestFile(t, root, ".eino-cli/memory/memory.json", "memory-1")
	assertTestFile(t, root, ".eino-cli/skill-build/artifact.txt", "artifact-1")
	assertMissing(t, root, ".eino-cli/skill-extra")
	assertTestFile(t, root, ".eino-cli/sessions/"+sid+"/runs/run-2.json", "run-2")
}

func writeTestFile(t *testing.T, root, rel, body string) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, root, rel, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", rel, got, want)
	}
}

func assertMissing(t *testing.T, root, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, rel)); !os.IsNotExist(err) {
		t.Fatalf("%s should be missing, err=%v", rel, err)
	}
}
