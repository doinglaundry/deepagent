package filesystem

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspace_PathAndSymlinkBoundaries(t *testing.T) {
	ctx := context.Background()
	rootDir := t.TempDir()
	outsideDir := t.TempDir()
	secretPath := filepath.Join(outsideDir, "secret.txt")
	secretWriteErr := os.WriteFile(secretPath, []byte("outside"), 0600)
	if secretWriteErr != nil {
		t.Fatal(secretWriteErr)
	}
	escapeSymlinkErr := os.Symlink(outsideDir, filepath.Join(rootDir, "escape"))
	if escapeSymlinkErr != nil {
		t.Fatal(escapeSymlinkErr)
	}
	localFilesystem := newTestLocalFilesystem(t, &LocalFilesystemConfig{RootDir: rootDir, VirtualMode: true})
	for _, path := range []string{"../secret.txt", "escape/secret.txt"} {
		_, readErr := localFilesystem.Read(ctx, path, nil, nil)
		if readErr == nil {
			t.Errorf("read escaped workspace: %q", path)
		}
		result, err := localFilesystem.Write(ctx, path, "changed")
		if err == nil && (result == nil || result.Error == "") {
			t.Errorf("write escaped workspace: %q", path)
		}
	}
	content, err := os.ReadFile(secretPath)
	if err != nil || string(content) != "outside" {
		t.Fatalf("outside file changed: %q %v", content, err)
	}
	_, writeErr := localFilesystem.Write(ctx, "inside.txt", "inside")
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	symlinkErr := os.Symlink("inside.txt", filepath.Join(rootDir, "safe-link"))
	if symlinkErr != nil {
		t.Fatal(symlinkErr)
	}
	contentText, err := localFilesystem.Read(ctx, "safe-link", nil, nil)
	if err != nil || contentText != "inside" {
		t.Fatalf("in-root symlink should work: %q %v", contentText, err)
	}
}

func TestReadFileLinesClampsMaxIntLimit(t *testing.T) {
	content := "first\nsecond\nthird\n"
	offset := 1
	limit := int(^uint(0) >> 1)

	window := ReadFileLines(content, &offset, &limit)
	if window != "second\nthird\n" {
		t.Fatalf("window = %q", window)
	}
}
