package local

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"eino-cli/deepagent/sandbox"
	"eino-cli/deepagent/sandbox/paths"
)

func TestSandboxWriteReadRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	mappings := []sandboxpaths.MountMapping{{VirtualPath: "/mnt/workspace", HostPath: tmp}}
	sb := newSandbox("test", "local:test", mappings)
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
	mappings := []sandboxpaths.MountMapping{{VirtualPath: "/mnt/skills", HostPath: tmp, ReadOnly: true}}
	sb := newSandbox("test", "local:test", mappings)
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
	mappings := []sandboxpaths.MountMapping{{VirtualPath: "/mnt/workspace", HostPath: tmp}}
	sb := newSandbox("test", "local:test", mappings)
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

func TestSandboxRejectsSymlinkEscapeForDirectOperations(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("outside"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.Symlink(outside, filepath.Join(root, "escape"))
	if err != nil {
		t.Skipf("symlink unsupported on this filesystem: %v", err)
	}

	mappings := []sandboxpaths.MountMapping{{VirtualPath: "/mnt/workspace", HostPath: root}}
	sb := newSandbox("test", "local:test", mappings)
	ctx := context.Background()

	_, err = sb.ReadFile(ctx, "/mnt/workspace/escape/secret.txt")
	if err == nil {
		t.Fatal("expected read through escaping symlink to fail")
	}
	err = sb.WriteFile(ctx, "/mnt/workspace/escape/new.txt", "pwn", false)
	if err == nil {
		t.Fatal("expected write through escaping symlink to fail")
	}
	err = sb.UpdateFile(ctx, "/mnt/workspace/escape/secret.txt", []byte("pwn"))
	if err == nil {
		t.Fatal("expected update through escaping symlink to fail")
	}
	_, err = sb.ListDir(ctx, "/mnt/workspace/escape", 2)
	if err == nil {
		t.Fatal("expected listing through escaping symlink to fail")
	}

	_, _, err = sb.Glob(ctx, "/mnt/workspace/escape", "*", sandbox.GlobOpts{})
	if err == nil {
		t.Fatal("glob followed escaping root")
	}
	_, _, err = sb.Grep(ctx, "/mnt/workspace/escape", "outside", sandbox.GrepOpts{})
	if err == nil {
		t.Fatal("grep followed escaping root")
	}

	data, err := os.ReadFile(filepath.Join(outside, "secret.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "outside" {
		t.Fatalf("outside sentinel changed to %q", data)
	}
}

func TestSandboxAllowsInRootSymlinkForDirectOperations(t *testing.T) {
	root := t.TempDir()
	err := os.Mkdir(filepath.Join(root, "inside"), 0o755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.Symlink("inside", filepath.Join(root, "alias"))
	if err != nil {
		t.Skipf("symlink unsupported on this filesystem: %v", err)
	}

	mappings := []sandboxpaths.MountMapping{{VirtualPath: "/mnt/workspace", HostPath: root}}
	sb := newSandbox("test", "local:test", mappings)
	ctx := context.Background()
	err = sb.WriteFile(ctx, "/mnt/workspace/alias/note.txt", "hello", false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sb.ReadFile(ctx, "/mnt/workspace/alias/note.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Fatalf("want hello, got %q", got)
	}
	entries, err := sb.ListDir(ctx, "/mnt/workspace/alias", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("expected in-root symlink target to be listable")
	}
}

func TestSandboxReadOnlyPolicyFollowsSymlinkAliases(t *testing.T) {
	parent := t.TempDir()
	writable := filepath.Join(parent, "writable")
	readonly := filepath.Join(parent, "readonly")
	err := os.Mkdir(writable, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.Mkdir(readonly, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	err = os.WriteFile(filepath.Join(readonly, "target.txt"), []byte("keep"), 0o644)
	if err != nil {
		t.Fatal(err)
	}
	err = os.Symlink("../readonly", filepath.Join(writable, "alias"))
	if err != nil {
		t.Skipf("symlink unsupported on this filesystem: %v", err)
	}

	mappings := []sandboxpaths.MountMapping{
		{VirtualPath: "/mnt/workspace", HostPath: writable},
		{VirtualPath: "/mnt/skills", HostPath: readonly, ReadOnly: true},
	}
	sb := newSandbox("test", "local:test", mappings)
	ctx := context.Background()

	err = sb.WriteFile(ctx, "/mnt/workspace/alias/target.txt", "changed", false)
	if err == nil {
		t.Fatal("expected aliased read-only write to fail")
	}
	var permissionErr *sandbox.PermissionError
	if !errors.As(err, &permissionErr) {
		t.Fatalf("want PermissionError, got %T", err)
	}
	err = sb.UpdateFile(ctx, "/mnt/workspace/alias/target.txt", []byte("changed"))
	if err == nil {
		t.Fatal("expected aliased read-only update to fail")
	}
	if !errors.As(err, &permissionErr) {
		t.Fatalf("want PermissionError, got %T", err)
	}
	data, err := os.ReadFile(filepath.Join(readonly, "target.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep" {
		t.Fatalf("read-only sentinel changed to %q", data)
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

func TestBuildPathMappingsIncludesRepo(t *testing.T) {
	mounts, err := sandboxpaths.BuildMountMappings("test-session")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range mounts {
		if m.VirtualPath == sandboxpaths.VirtualPathPrefixRepo {
			found = true
		}
	}
	if !found {
		t.Fatal("expected VirtualPathPrefixRepo mapping")
	}
}

func TestSandboxRejectsReplacedMount(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "workspace")
	err := os.Mkdir(root, 0700)
	if err != nil {
		t.Fatal(err)
	}
	sb := newSandbox("test", "local:test", []sandboxpaths.MountMapping{{VirtualPath: "/mnt/workspace", HostPath: root}})
	err = os.Rename(root, root+"-old")
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	err = os.Symlink(outside, root)
	if err != nil {
		t.Skip(err)
	}
	err = sb.WriteFile(context.Background(), "/mnt/workspace/new.txt", "escape", false)
	if err == nil {
		t.Fatal("write accepted replaced mount")
	}
	_, _, err = sb.Glob(context.Background(), "/mnt/workspace", "*", sandbox.GlobOpts{})
	if err == nil {
		t.Fatal("glob accepted replaced mount")
	}
	_, err = os.Stat(filepath.Join(outside, "new.txt"))
	if !os.IsNotExist(err) {
		t.Fatalf("outside path touched: %v", err)
	}
}
