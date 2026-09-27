package backend

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspace_PathAndSymlinkBoundaries(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	b := mustLocalFilesystem(t, &LocalFilesystemConfig{RootDir: root, VirtualMode: true})
	for _, path := range []string{"../secret.txt", "escape/secret.txt"} {
		if _, err := b.Read(ctx, path, nil, nil); err == nil {
			t.Errorf("read escaped workspace: %q", path)
		}
		result, err := b.Write(ctx, path, "changed")
		if err == nil && (result == nil || result.Error == "") {
			t.Errorf("write escaped workspace: %q", path)
		}
	}
	content, err := os.ReadFile(secret)
	if err != nil || string(content) != "outside" {
		t.Fatalf("outside file changed: %q %v", content, err)
	}
	if _, err := b.Write(ctx, "inside.txt", "inside"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside.txt", filepath.Join(root, "safe-link")); err != nil {
		t.Fatal(err)
	}
	contentText, err := b.Read(ctx, "safe-link", nil, nil)
	if err != nil || contentText != "inside" {
		t.Fatalf("in-root symlink should work: %q %v", contentText, err)
	}
}
