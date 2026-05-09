package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// pngHeader is the 8-byte PNG file signature.
var pngHeader = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

func TestReadImage_PNG(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "icon.png")
	body := append(pngHeader, []byte("fake-png-payload")...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	data, mime, err := readImage(context.Background(), tmp, "icon.png")
	if err != nil {
		t.Fatalf("readImage: %v", err)
	}
	if string(data) != string(body) {
		t.Errorf("bytes round-trip mismatch")
	}
	if mime != "image/png" {
		t.Errorf("mime = %q, want image/png", mime)
	}
}

func TestReadImage_NonImageRejected(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "notes.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, _, err := readImage(context.Background(), tmp, "notes.txt"); err == nil {
		t.Fatal("expected error for non-image file")
	}
}

// TestReadImage_MissingFile mirrors os.Stat's not-found path.
func TestReadImage_MissingFile(t *testing.T) {
	if _, _, err := readImage(context.Background(), t.TempDir(), "ghost.png"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestReadImage_EmptyPathRejected covers the "must not be empty" guard.
func TestReadImage_EmptyPathRejected(t *testing.T) {
	if _, _, err := readImage(context.Background(), t.TempDir(), ""); err == nil {
		t.Fatal("expected error for empty path")
	}
}

// TestReadImage_AbsolutePathHonoured: absolute paths are NOT re-rooted under root.
func TestReadImage_AbsolutePathHonoured(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "outside.png")
	body := append(pngHeader, 0xff)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	data, mime, err := readImage(context.Background(), t.TempDir(), path)
	if err != nil {
		t.Fatalf("readImage absolute: %v", err)
	}
	if string(data) != string(body) {
		t.Errorf("bytes mismatch on absolute path")
	}
	if mime != "image/png" {
		t.Errorf("mime = %q, want image/png", mime)
	}
}

// TestReadImage_EmptyRootFallsBackToCwd: empty root must resolve against os.Getwd().
func TestReadImage_EmptyRootFallsBackToCwd(t *testing.T) {
	tmp := t.TempDir()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	body := append(pngHeader, 0x00)
	if err := os.WriteFile("cwd.png", body, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	data, mime, err := readImage(context.Background(), "", "cwd.png")
	if err != nil {
		t.Fatalf("readImage with empty root: %v", err)
	}
	if string(data) != string(body) {
		t.Errorf("bytes mismatch on cwd-rooted lookup")
	}
	if mime != "image/png" {
		t.Errorf("mime = %q, want image/png", mime)
	}
}
