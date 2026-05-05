package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/adk/filesystem"
)

func TestLocalSandbox_BackendShellMounts(t *testing.T) {
	tmp := t.TempDir()
	mounts := []Mount{
		{ContainerPath: "/mnt/data", ReadOnly: true},
	}
	sb := NewLocalSandbox(tmp).WithMounts(mounts)

	if sb.WorkingDir() != tmp {
		t.Fatalf("WorkingDir: got %q, want %q", sb.WorkingDir(), tmp)
	}
	if got := sb.Mounts(); len(got) != 1 || got[0].ContainerPath != "/mnt/data" {
		t.Fatalf("Mounts: got %v, want one /mnt/data entry", got)
	}

	if err := os.WriteFile(filepath.Join(tmp, "hello.txt"), []byte("hi"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	backend := sb.Backend()
	if backend == nil {
		t.Fatal("Backend() returned nil")
	}
	out, err := backend.Read(context.Background(), &filesystem.ReadRequest{FilePath: "hello.txt"})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if out.Content != "hi" {
		t.Fatalf("Read content: got %q, want %q", out.Content, "hi")
	}

	if sb.Shell() == nil {
		t.Fatal("Shell() returned nil")
	}
}

func TestLocalSandbox_FallbackWhenRootEmpty(t *testing.T) {
	sb := NewLocalSandbox("")
	if sb.WorkingDir() == "" {
		t.Fatal("WorkingDir should fall back to a non-empty value")
	}
}
