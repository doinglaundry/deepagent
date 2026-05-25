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
	mappings := []PathMapping{{ContainerPath: "/mnt/workspace", LocalPath: tmp}}
	sb := newSandbox("test", "local:test", ptrMappings(mappings))
	ctx := context.Background()
	if err := sb.WriteFile(ctx, "/mnt/workspace/note.txt", "hello", false); err != nil {
		t.Fatal(err)
	}
	got, err := sb.ReadFile(ctx, "/mnt/workspace/note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Fatalf("want hello, got %q", got)
	}
	if sb.SessionID() != "test" {
		t.Fatalf("SessionID = %q, want test", sb.SessionID())
	}
}

func TestSandboxReadOnlyMountBlocksWrite(t *testing.T) {
	tmp := t.TempDir()
	mappings := []PathMapping{{ContainerPath: "/mnt/skills", LocalPath: tmp, ReadOnly: true}}
	sb := newSandbox("test", "local:test", ptrMappings(mappings))
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
	mappings := []PathMapping{{ContainerPath: "/mnt/workspace", LocalPath: tmp}}
	sb := newSandbox("test", "local:test", ptrMappings(mappings))
	entries, err := sb.ListDir(context.Background(), "/mnt/workspace", 2)
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

func TestManagerReturnsStartupSandbox(t *testing.T) {
	mgr, err := New("session-a")
	if err != nil {
		t.Fatal(err)
	}
	if mgr.SessionID() != "session-a" {
		t.Fatalf("SessionID = %q", mgr.SessionID())
	}
	sid, err := mgr.GetSandboxIdBySessionId(context.Background(), "session-a")
	if err != nil {
		t.Fatal(err)
	}
	got, err := mgr.Get(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID() != sid || got.SessionID() != "session-a" {
		t.Fatalf("Get returned %#v, want sandbox id %q session session-a", got, sid)
	}
}

func TestManagerRejectsForeignSession(t *testing.T) {
	mgr, err := New("session-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.GetSandboxIdBySessionId(context.Background(), "other"); err == nil {
		t.Fatal("expected error for foreign session_id")
	}
}

func TestManagerGetDoesNotDeadlock(t *testing.T) {
	mgr, err := New("session-a")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := mgr.GetSandboxIdBySessionId(context.Background(), "session-a")
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
		t.Fatal("Get deadlocked")
	}
	if getErr != nil {
		t.Fatal(getErr)
	}
	if got.SessionID() != "session-a" {
		t.Fatalf("SessionID = %q", got.SessionID())
	}
}

func ptrMappings(in []PathMapping) []*PathMapping {
	out := make([]*PathMapping, len(in))
	for i := range in {
		m := in[i]
		out[i] = &m
	}
	return out
}
