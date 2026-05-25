package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eino-cli/backend/sandbox"
)

func TestSandboxWriteReadRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	mappings := []PathMapping{{ContainerPath: "/mnt/user-data", LocalPath: tmp}}
	sb := newSandbox("local:test", mappings)
	ctx := context.Background()
	if err := sb.WriteFile(ctx, "/mnt/user-data/note.txt", "hello", false); err != nil {
		t.Fatal(err)
	}
	got, err := sb.ReadFile(ctx, "/mnt/user-data/note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Fatalf("want hello, got %q", got)
	}
}

func TestSandboxReadOnlyMountBlocksWrite(t *testing.T) {
	tmp := t.TempDir()
	mappings := []PathMapping{{ContainerPath: "/mnt/skills", LocalPath: tmp, ReadOnly: true}}
	sb := newSandbox("local:test", mappings)
	err := sb.WriteFile(context.Background(), "/mnt/skills/hack.txt", "x", false)
	if err == nil {
		t.Fatal("expected permission error")
	}
	var perm *sandbox.PermissionError
	if !errors.As(err, &perm) {
		t.Fatalf("want PermissionError, got %T", err)
	}
}

func TestSandboxListDir(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "a.txt"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "b.log"), []byte("2"), 0o644); err != nil {
		t.Fatal(err)
	}
	mappings := []PathMapping{{ContainerPath: "/mnt/user-data", LocalPath: tmp}}
	sb := newSandbox("local:test", mappings)
	entries, err := sb.ListDir(context.Background(), "/mnt/user-data", 2)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if strings.HasSuffix(e, "/a.txt") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a.txt in entries, got %v", entries)
	}
}

func TestManagerGetSessionSandboxDoesNotDeadlock(t *testing.T) {
	mgr, err := New()
	if err != nil {
		t.Fatal(err)
	}
	sid, err := mgr.Acquire(context.Background(), "session-a")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var got sandbox.Sandbox
	var getErr error
	go func() {
		got, getErr = mgr.Get(context.Background(), sid)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Get deadlocked for session sandbox")
	}
	if getErr != nil {
		t.Fatal(getErr)
	}
	if got == nil || got.ID() != sid {
		t.Fatalf("Get returned %v, want sandbox id %q", got, sid)
	}
}
