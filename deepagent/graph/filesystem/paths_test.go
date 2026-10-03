package filesystem

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
	writeErr2 := os.WriteFile(secret, []byte("outside"), 0600)
	if writeErr2 != nil {
		t.Fatal(writeErr2)
	}
	symlinkErr2 := os.Symlink(outside, filepath.Join(root, "escape"))
	if symlinkErr2 != nil {
		t.Fatal(symlinkErr2)
	}
	b := mustLocalFilesystem(t, &LocalFilesystemConfig{RootDir: root, VirtualMode: true})
	for _, path := range []string{"../secret.txt", "escape/secret.txt"} {
		_, readErr := b.Read(ctx, path, nil, nil)
		if readErr == nil {
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
	_, writeErr := b.Write(ctx, "inside.txt", "inside")
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	symlinkErr := os.Symlink("inside.txt", filepath.Join(root, "safe-link"))
	if symlinkErr != nil {
		t.Fatal(symlinkErr)
	}
	contentText, err := b.Read(ctx, "safe-link", nil, nil)
	if err != nil || contentText != "inside" {
		t.Fatalf("in-root symlink should work: %q %v", contentText, err)
	}
}

func TestReadFileLinesClampsMaxIntLimit(t *testing.T) {
	content := "first\nsecond\nthird\n"
	offset := 1
	limit := int(^uint(0) >> 1)

	got := ReadFileLines(content, &offset, &limit)
	if got != "second\nthird\n" {
		t.Fatalf("window = %q", got)
	}
}
