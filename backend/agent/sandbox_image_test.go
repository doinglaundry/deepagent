package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// pngHeader is the 8-byte PNG file signature. Any DetectContentType or
// extension fallback should classify a file starting with these bytes
// as image/png.
var pngHeader = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}

// TestLocalSandbox_ReadImage_PNG verifies a real PNG file round-trips
// through the sandbox's image accessor: bytes preserved + image/png mime.
func TestLocalSandbox_ReadImage_PNG(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "icon.png")
	body := append(pngHeader, []byte("fake-png-payload")...)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	sb := NewLocalSandbox(tmp)
	data, mime, err := sb.ReadImage(context.Background(), "icon.png")
	if err != nil {
		t.Fatalf("ReadImage: %v", err)
	}
	if string(data) != string(body) {
		t.Errorf("bytes round-trip mismatch")
	}
	if mime != "image/png" {
		t.Errorf("mime = %q, want image/png", mime)
	}
}

// TestLocalSandbox_ReadImage_NonImageRejected verifies the accessor
// refuses files whose sniffed MIME isn't image/* and whose extension
// doesn't rescue them.
func TestLocalSandbox_ReadImage_NonImageRejected(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "notes.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	sb := NewLocalSandbox(tmp)
	_, _, err := sb.ReadImage(context.Background(), "notes.txt")
	if err == nil {
		t.Fatal("expected error for non-image file")
	}
}

// TestLocalSandbox_ReadImage_MissingFile mirrors os.Stat's not-found path.
func TestLocalSandbox_ReadImage_MissingFile(t *testing.T) {
	sb := NewLocalSandbox(t.TempDir())
	_, _, err := sb.ReadImage(context.Background(), "ghost.png")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestLocalSandbox_ReadImage_EmptyPathRejected covers the "must not be
// empty" guard.
func TestLocalSandbox_ReadImage_EmptyPathRejected(t *testing.T) {
	sb := NewLocalSandbox(t.TempDir())
	_, _, err := sb.ReadImage(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

// TestLocalSandbox_ReadImage_AbsolutePathHonoured ensures absolute paths
// are NOT re-rooted under the sandbox.
func TestLocalSandbox_ReadImage_AbsolutePathHonoured(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "outside.png")
	body := append(pngHeader, 0xff)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	sb := NewLocalSandbox(t.TempDir()) // different root
	data, mime, err := sb.ReadImage(context.Background(), path)
	if err != nil {
		t.Fatalf("ReadImage absolute: %v", err)
	}
	if string(data) != string(body) {
		t.Errorf("bytes mismatch on absolute path")
	}
	if mime != "image/png" {
		t.Errorf("mime = %q, want image/png", mime)
	}
}
