package local

import (
	"path/filepath"
	"strings"
	"testing"

	"eino-cli/backend/sandbox"
	"eino-cli/backend/sandboxpaths"
)

func TestResolvePathPicksMostSpecific(t *testing.T) {
	tmp := t.TempDir()
	mappings := []sandboxpaths.MountMapping{
		{VirtualPath: "/mnt/workspace", HostPath: filepath.Join(tmp, "root")},
		{VirtualPath: "/mnt/workspace/deep", HostPath: filepath.Join(tmp, "root", "deep")},
	}
	resolvedPath, err := resolvePath(mappings, "/mnt/workspace/deep/foo.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(resolvedPath.HostPath, filepath.Join("root", "deep", "foo.txt")) {
		t.Fatalf("expected deeper mapping to win, got %s", resolvedPath.HostPath)
	}
}

func TestResolvePathRejectsEscape(t *testing.T) {
	tmp := t.TempDir()
	mappings := []sandboxpaths.MountMapping{
		{VirtualPath: "/mnt/workspace", HostPath: tmp},
	}
	_, err := resolvePath(mappings, "/mnt/workspace/../../etc/passwd")
	if err == nil {
		t.Fatal("expected escape error, got nil")
	}
}

func TestReverseResolvePathMapsBack(t *testing.T) {
	tmp := t.TempDir()
	abs, _ := filepath.Abs(tmp)
	mappings := []sandboxpaths.MountMapping{
		{VirtualPath: "/mnt/workspace", HostPath: abs},
	}
	hostPath := filepath.Join(abs, "subdir", "file.txt")
	got := sandbox.ReverseResolvePath(mappings, hostPath)
	want := "/mnt/workspace/subdir/file.txt"
	if got != want {
		t.Fatalf("reverse: want %q, got %q", want, got)
	}
}

func TestMaskHostPathsInOutput(t *testing.T) {
	tmp := t.TempDir()
	abs, _ := filepath.Abs(tmp)
	mappings := []sandboxpaths.MountMapping{
		{VirtualPath: "/mnt/workspace", HostPath: abs},
	}
	out := "log: " + filepath.Join(abs, "foo.txt") + " done"
	masked := sandbox.MaskHostPathsInOutput(mappings, out)
	if !strings.Contains(masked, "/mnt/workspace/foo.txt") {
		t.Fatalf("expected mask, got %q", masked)
	}
}

func TestReadOnlyPath(t *testing.T) {
	tmp := t.TempDir()
	abs, _ := filepath.Abs(tmp)
	mappings := []sandboxpaths.MountMapping{{VirtualPath: "/mnt/skills", HostPath: abs, ReadOnly: true}}
	if !isReadOnlyPath(mappings, filepath.Join(abs, "x")) {
		t.Fatal("expected read-only")
	}
}
