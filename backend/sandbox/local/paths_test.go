package local

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathPicksMostSpecific(t *testing.T) {
	tmp := t.TempDir()
	mappings := []PathMapping{
		{ContainerPath: "/mnt/user-data", LocalPath: filepath.Join(tmp, "u")},
		{ContainerPath: "/mnt/user-data/workspace", LocalPath: filepath.Join(tmp, "u", "workspace")},
	}
	r, err := resolvePath(mappings, "/mnt/user-data/workspace/foo.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(r.Path, filepath.Join("u", "workspace", "foo.txt")) {
		t.Fatalf("expected workspace mapping to win, got %s", r.Path)
	}
}

func TestResolvePathRejectsEscape(t *testing.T) {
	tmp := t.TempDir()
	mappings := []PathMapping{
		{ContainerPath: "/mnt/user-data", LocalPath: tmp},
	}
	_, err := resolvePath(mappings, "/mnt/user-data/../../etc/passwd")
	if err == nil {
		t.Fatal("expected escape error, got nil")
	}
}

func TestReverseResolvePathMapsBack(t *testing.T) {
	tmp := t.TempDir()
	abs, _ := filepath.Abs(tmp)
	mappings := []PathMapping{
		{ContainerPath: "/mnt/user-data", LocalPath: abs},
	}
	hostPath := filepath.Join(abs, "subdir", "file.txt")
	got := reverseResolvePath(mappings, hostPath)
	want := "/mnt/user-data/subdir/file.txt"
	if got != want {
		t.Fatalf("reverse: want %q, got %q", want, got)
	}
}

func TestReverseResolveInOutputMasksHostPaths(t *testing.T) {
	tmp := t.TempDir()
	abs, _ := filepath.Abs(tmp)
	mappings := []PathMapping{
		{ContainerPath: "/mnt/user-data", LocalPath: abs},
	}
	out := "log: " + filepath.Join(abs, "foo.txt") + " done"
	masked := reverseResolvePathsInOutput(mappings, out)
	if !strings.Contains(masked, "/mnt/user-data/foo.txt") {
		t.Fatalf("expected mask, got %q", masked)
	}
}

func TestReadOnlyPath(t *testing.T) {
	tmp := t.TempDir()
	abs, _ := filepath.Abs(tmp)
	mappings := []PathMapping{{ContainerPath: "/mnt/skills", LocalPath: abs, ReadOnly: true}}
	if !isReadOnlyPath(mappings, filepath.Join(abs, "x")) {
		t.Fatal("expected read-only")
	}
}
